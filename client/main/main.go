package main

import (
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/persistence"
	"github.com/dumacp/go-hikvision/client"
	"github.com/dumacp/go-hikvision/client/messages"
	"github.com/dumacp/go-hikvision/client/parametros"
	"golang.org/x/exp/errors/fmt"
)

const (
	showVersion = "1.0.41"
)

var debug bool
var logStd bool
var socket string
var pathdb string
var version bool
var logXML bool

var encryptCreds bool

var videoDir string
var videoPreRoll time.Duration
var videoDuration time.Duration
var videoBurstGap time.Duration
var videoMaxDuration time.Duration
var videoQueue int
var videoMinFreeMB int64
var withoutEventID bool

var ntpServer string
var ntpPort int
var ntpInterval time.Duration
var ntpTimeZone string
var ntpDriftMax time.Duration
var cameraCheckInterval time.Duration
var recordStart string
var recordEnd string
var smartCodec string
var videoCodec string
var videoFrameRate int
var videoGop int
var videoQuality int
var videoBitrateMax int
var encoderMinUptime time.Duration

var cameraPowerGPIO int
var cameraPowerOffLevel int
var cameraPowerSet int
var gpioRegisters int
var cameraPowerOffDelay time.Duration
var cameraBootDelay time.Duration

var deviceID string
var videoEnabled bool
var platformConfigCache string
var disablePlatformConfig bool

var isZeroOpenState zeroFlags
var enableCountWithCloseDoor closeFlags
var cameras cameraFlags

func init() {
	flag.BoolVar(&debug, "debug", false, "debug enable")
	// Configuracion bajada de la plataforma (DEV_CAMERACOUNT_CONFIG). Ver
	// client/parametros y client/configuracion.go.
	flag.StringVar(&deviceID, "id", "",
		"device id; it is the key of the platform parameter map. Empty uses the gateway hostname")
	flag.StringVar(&platformConfigCache, "platformConfigCache", "/SD/boltdbs/cameracount-config.json",
		"file where the last good platform configuration is cached, so a boot without "+
			"network keeps it. Empty disables the cache")
	flag.BoolVar(&disablePlatformConfig, "disablePlatformConfig", false,
		"do not download configuration from the platform; the binary behaves exactly as "+
			"before this feature existed")
	flag.BoolVar(&videoEnabled, "videoEnabled", true,
		"enable the video clip extraction. It still needs -videoDir, -camera and credentials; "+
			"this switch is what the platform can turn off remotely")
	// Compuerta de compatibilidad, temporal y a propósito: la plataforma todavía no
	// procesa los mensajes que traen event_id, y sin este flag no hay forma de probar
	// el resto del binario en un vehículo real sin romperle el conteo a la plataforma.
	// Se quita el día que la plataforma lo acepte; entonces el binario corre sin él.
	flag.BoolVar(&withoutEventID, "withoutEventID", false,
		"omit the event_id field from the published COUNTERSDOOR; temporary compatibility "+
			"switch for platforms that cannot yet process it")
	flag.BoolVar(&logStd, "logStd", false, "log in stderr")
	flag.StringVar(&socket, "socket", ":8088", "socket to listen events")
	flag.StringVar(&pathdb, "pathdb", "/SD/boltdbs/countingdb", "socket to listen events")
	flag.BoolVar(&version, "version", false, "show version")
	flag.BoolVar(&logXML, "logxml", false, "logging XML in file")
	flag.BoolVar(&encryptCreds, "encryptCredentials", false,
		"read user and password from stdin (one per line) and print the "+envCredentials+" value")
	flag.Var(&isZeroOpenState, "zeroOpenState",
		"is Zero the open state? A single occurrence applies to ALL doors; two or more are "+
			"positional and the n-th configures door n-1")
	flag.Var(&enableCountWithCloseDoor, "countWithCloseDoor",
		"enable count with close door? Same rule as -zeroOpenState: one occurrence covers "+
			"every door, two or more are positional")
	flag.Var(&cameras, "camera",
		"camera address of a door, as \"ip:door\" (e.g. 192.168.188.21:1). Without the \":door\" "+
			"suffix it is positional and the n-th occurrence is door n-1; the two forms cannot "+
			"be mixed")
	flag.StringVar(&videoDir, "videoDir", "",
		"directory to store the video clip of every pass; empty disables the extraction")
	// 10s de pre-roll medidos contra tráfico real: el dateTime del evento llega entre
	// 2 y 7 segundos DESPUÉS del cruce físico, así que con 5s el cruce quedaba en el
	// borde del clip o afuera. Con 10s/16s los tres cruces de una fila entraron en los
	// segundos 7 a 11 de la ventana.
	flag.DurationVar(&videoPreRoll, "videoPreRoll", 10*time.Second,
		"how much video to keep before the event, so the crossing itself is in the clip")
	flag.DurationVar(&videoDuration, "videoDuration", 16*time.Second,
		"clip duration of a single pass: -videoPreRoll before it and the rest after it")
	// Encadenar ráfagas: medido el 2026-11-07, 6 pasos en 27 s daban 5 clips casi iguales
	// con la ventana fija; encadenados son uno de 43 s y la mitad del tamaño.
	flag.DurationVar(&videoBurstGap, client.FlagVideoBurstGap, 10*time.Second,
		"a pass up to this long after the previous one of the same door goes to the same clip, "+
			"which grows to keep video after it; 0 disables it (one fixed window per clip)")
	flag.DurationVar(&videoMaxDuration, client.FlagVideoMaxDuration, 60*time.Second,
		"longest clip a burst can grow to; past it the next pass opens a new clip")
	flag.IntVar(&videoQueue, "videoQueue", 500,
		"maximum pending extractions; playback runs in real time, so a burst queues up")
	// El piso protege la boltdb del conteo, que vive en la misma partición: quedarse sin
	// video de un pasajero cuesta un clip, quedarse sin disco cuesta el acumulado del
	// vehículo entero. Bajo el piso se deja de extraer, nunca se borra ni se sobrescribe.
	flag.Int64Var(&videoMinFreeMB, "videoMinFree", client.VideoMinFreeDefault>>20,
		"free space floor in MiB on the -videoDir partition; below it the extraction stops "+
			"(no clip is ever deleted or overwritten). 0 disables the check")
	flag.StringVar(&ntpServer, "ntpServer", "",
		"NTP server the cameras must use; empty disables the time and NTP maintenance")
	flag.IntVar(&ntpPort, "ntpPort", 123, "NTP server port")
	// El intervalo va en minutos en ISAPI. Un equipo verificado venía con 1500, que son
	// 25 horas entre sincronizaciones: NTP configurado y aun así derivando.
	flag.DurationVar(&ntpInterval, "ntpInterval", 60*time.Minute,
		"how often the camera synchronizes; ISAPI stores it in minutes")
	// POSIX invierte el signo: CST+5:00:00 es UTC-5. Escribirlo como -5:00:00 deja la
	// cámara diez horas corrida.
	flag.StringVar(&ntpTimeZone, "timeZone", "CST+5:00:00",
		"POSIX time zone for the cameras; the sign is inverted, CST+5:00:00 means UTC-5")
	flag.DurationVar(&ntpDriftMax, "timeDriftMax", 10*time.Second,
		"drift tolerated before reporting a camera clock as off")
	flag.DurationVar(&cameraCheckInterval, "cameraCheckInterval", 30*time.Minute,
		"how often the camera time and NTP configuration is checked")
	// Un evento fuera de la ventana de grabación no tiene video posible: la cámara
	// verificada venía con 07:55-16:02 y dejaba media jornada sin nada que extraer. No
	// conviene 24/7 sin pensarlo: con 507 kbps medidos, una SD de 8 GB da 1.4 días de
	// retención grabando todo el día contra 1.7 grabando veinte horas. La palanca real de
	// la retención es el tamaño de la tarjeta, no el horario.
	flag.StringVar(&recordStart, "recordStart", "04:00:00",
		"start of the camera recording window, camera local time; empty leaves it as is")
	flag.StringVar(&recordEnd, "recordEnd", "23:59:00",
		"end of the recording window; the camera rounds to the minute, so :59 becomes :00")
	// SmartCodec (H.264+) es lo que dejaba los clips congelados: con escena quieta la
	// cámara grababa un keyframe cada varios segundos. Medido: 49 frames con un hueco de
	// 5.7s activo, contra 201 continuos apagado. Vacío deja la cámara como esté, porque
	// tocar el encoder obliga a reiniciar.
	flag.StringVar(&smartCodec, "smartCodec", "",
		"\"off\" to disable H.264+ (recommended when extracting video), \"on\" to enable it; "+
			"empty leaves it as is")
	// Medido en la misma cámara, misma escena, fixedQuality 40, ventana de 12 s:
	//
	//   H.264 con H.264+   25.9 KB/s   pero el clip sale congelado casi siempre
	//   H.264 sin H.264+   42.9 KB/s   completo
	//   H.265 sin H.264+   28.2 KB/s   completo
	//   H.265 sin H.264+, 12 fps   16.7 KB/s   completo   <- el mejor de los dos mundos
	//
	// El extractor maneja los dos codecs, y la SD puede tener grabaciones de antes y
	// después del cambio: cada clip se extrae con el codec que la cámara ofrezca.
	flag.StringVar(&videoCodec, "videoCodec", "",
		"video codec for the main stream: \"h265\" is smaller than h264 at equal quality "+
			"(measured 16.7 vs 42.9 KB/s at 12 fps); empty leaves it as is")
	flag.IntVar(&videoFrameRate, "videoFrameRate", 0,
		"frames per second for the main stream; 0 leaves it as is (ISAPI stores centi-fps)")
	flag.IntVar(&videoGop, "videoGop", 0,
		"GOP length in frames; 0 leaves it as is. A shorter GOP means finer seeking")
	// fixedQuality es la palanca que de verdad manda el tamaño en la cámara verificada. El
	// modelo admite 1, 20, 40, 60, 80 y 100, y viene en 40; un valor fuera de esa lista se
	// guarda recortado respondiendo OK, así que el actor relee después de escribir.
	flag.IntVar(&videoQuality, "videoQuality", 0,
		"VBR quality of the main stream, 1..100 (the verified model accepts 1,20,40,60,80,100); "+
			"lower means smaller files; 0 leaves it as is")
	// El techo de bitrate no baja el tamaño típico: acota el peor caso. En la cámara
	// verificada viene en 8192 kbps mientras graba a ~200, así que no ata nada.
	flag.IntVar(&videoBitrateMax, "videoBitrateMax", 0,
		"VBR bitrate ceiling in kbps, 32..16384; bounds the worst case, does not lower the "+
			"typical size; 0 leaves it as is")
	// Este mínimo es el ÚNICO freno contra un bucle de reinicios de cámara: el tope de "un
	// reinicio por cámara" vive en memoria y se pierde al arrancar de nuevo, así que un
	// binario que reinicia en bucle solo se detiene si nunca alcanza este umbral.
	flag.DurationVar(&encoderMinUptime, "encoderMinUptime", client.EncoderMinUptimeDefault,
		"how long the binary must be up before touching the encoder profile; 0 applies it on "+
			"the first cycle. It is the only brake against a camera reboot loop")

	// Apagado de la camara comandado por go-ignition. Sin -cameraPowerGPIO no se crea
	// el actor, no se escucha el topico y el binario se comporta como antes.
	flag.IntVar(&cameraPowerGPIO, "cameraPowerGPIO", 0,
		"sysfs GPIO line that controls the camera power; 0 disables the whole feature. "+
			"When set, the binary listens to EVENTS/ignition and cuts power when the "+
			"vehicle is switched off")
	flag.IntVar(&cameraPowerOffLevel, "cameraPowerOffLevel", 1,
		"level (0 or 1) that means POWER OFF on -cameraPowerGPIO; the complement means "+
			"power on and is what the line is driven to at startup")
	flag.IntVar(&gpioRegisters, "gpioRegisters", -1,
		"read the SoC registers (GDIR, DR, PSR) of a sysfs GPIO line and exit. Diagnostic "+
			"only, it writes nothing: the sysfs value file returns the pad level on i.MX, so it "+
			"cannot tell \"the software did not drive the pin\" from \"it did and the "+
			"hardware does not follow\". Comparing DR against PSR can")
	flag.IntVar(&cameraPowerSet, "cameraPowerSet", -1,
		"drive -cameraPowerGPIO to this level (0 or 1) and exit, WITHOUT starting the "+
			"counter. Use it against the real vehicle to find out which level powers the "+
			"camera off, then set -cameraPowerOffLevel to it")
	flag.DurationVar(&cameraPowerOffDelay, "cameraPowerOffDelay", 20*time.Second,
		"how long to wait after the shutdown signal before cutting power, so the clip "+
			"being extracted can finish. Extraction runs in real time, so a 16s clip "+
			"takes 16s")
	flag.DurationVar(&cameraBootDelay, "cameraBootDelay", 2*time.Minute,
		"how long the camera takes to boot after power returns; measured at ~2min on the "+
			"DS-2XM6825G0. Until it elapses the binary does not talk to the camera, to "+
			"avoid reporting a fault that is just a boot")
}

func main() {

	flag.Parse()

	if version {
		fmt.Printf("version: %s\n", showVersion)
		os.Exit(2)
	}

	if encryptCreds {
		os.Exit(printEncryptedCredentials())
	}

	// Diagnostico de solo lectura, antes que nada porque no toca el estado.
	if gpioRegisters >= 0 {
		os.Exit(mostrarRegistrosGPIO(gpioRegisters))
	}

	// Modo utilitario: deja la linea en el nivel pedido y termina. Va junto a
	// -encryptCredentials y antes de cualquier otra cosa, porque no levanta el conteo.
	if cameraPowerSet >= 0 {
		os.Exit(aplicarNivelGPIO(cameraPowerGPIO, cameraPowerSet))
	}

	initLogs(debug, logStd, logXML)

	// Una lista de cámaras mal escrita se detiene acá y no arranca a medias: contar la
	// puerta equivocada durante un turno completo es peor que no arrancar, porque los
	// contadores publicados quedan mal y no hay forma de repararlos después.
	camerasByDoor, err := resolveCameras(cameras)
	if err != nil {
		log.Fatalln(err)
	}

	// El perfil de codificación se valida acá y no en el actor: un valor mal escrito que
	// se ignore en silencio deja la cámara con el ajuste viejo mientras la configuración
	// dice otra cosa, y eso se descubre recién al ver un clip congelado.
	videoCodec, err = client.NormalizeVideoCodec(videoCodec)
	if err != nil {
		log.Fatalln(err)
	}
	if _, err := client.NormalizeSmartCodec(smartCodec); err != nil {
		log.Fatalln(err)
	}
	if videoQuality < 0 || videoQuality > 100 {
		log.Fatalf("-videoQuality %d está fuera de 1..100", videoQuality)
	}
	if videoBitrateMax != 0 && (videoBitrateMax < 32 || videoBitrateMax > 16384) {
		log.Fatalf("-videoBitrateMax %d está fuera de 32..16384 kbps", videoBitrateMax)
	}
	if encoderMinUptime < 0 {
		log.Fatalf("-encoderMinUptime %v no puede ser negativo", encoderMinUptime)
	}
	if videoMinFreeMB < 0 {
		log.Fatalf("-videoMinFree %d no puede ser negativo", videoMinFreeMB)
	}
	// Un valor mal escrito detiene el arranque, igual que -videoCodec y -smartCodec.
	// Ignorarlo en silencio dejaria la camara sin control de energia mientras la
	// configuracion dice lo contrario, y eso se descubre cuando la bateria ya se gasto.
	if cameraPowerGPIO < 0 {
		log.Fatalf("-cameraPowerGPIO %d no puede ser negativo", cameraPowerGPIO)
	}
	if cameraPowerOffLevel != 0 && cameraPowerOffLevel != 1 {
		log.Fatalf("-cameraPowerOffLevel %d invalido, solo 0 o 1", cameraPowerOffLevel)
	}
	if cameraPowerOffDelay < 0 {
		log.Fatalf("-cameraPowerOffDelay %v no puede ser negativo", cameraPowerOffDelay)
	}
	if cameraBootDelay < 0 {
		log.Fatalf("-cameraBootDelay %v no puede ser negativo", cameraBootDelay)
	}
	videoMinFree := uint64(videoMinFreeMB) << 20

	// Credentials are only needed to talk to the camera, so a missing or unreadable
	// value must not stop the counting: warn and keep going.
	camUser, camPass, err := credentialsFromEnv()
	switch {
	case err != nil:
		warnlog.Printf("%s: %s", envCredentials, err)
	case len(camUser) == 0:
		infolog.Printf("%s not set, camera dialogue disabled", envCredentials)
	default:
		infolog.Printf("camera credentials loaded for user %q", camUser)
	}

	// peoplecounting.Listen(socket, errlog)

	provider, err := newProvider(pathdb, 10)
	if err != nil {
		log.Fatalln(err)
	}

	sistema := actor.NewActorSystem()
	rootContext := sistema.Root

	if len(isZeroOpenState) <= 0 {
		isZeroOpenState = []bool{false}
	}
	if len(enableCountWithCloseDoor) <= 0 {
		enableCountWithCloseDoor = []bool{false}
	}

	if len(camerasByDoor) == 0 {
		infolog.Printf("cameras: not configured, using the legacy rule (%s -> door 1, rest -> door 0)",
			client.LegacyBackDoorIP())
	}

	// Prioridad de las fuentes, deliberadamente la INVERSA de la del resto de la flota:
	// el flag pasado explícitamente le gana a la plataforma, que solo rellena lo que el
	// flag no fijó. Ver client.Resolver y flagsPasados().
	fijados := flagsPasados()
	base := configuracionBase(camerasByDoor)

	// El cache en disco se lee ANTES de arrancar los actores. La plataforma tarda ~10 s
	// en llegar y puede no llegar nunca; sin esto, un arranque sin red deja el equipo con
	// los defaults, y el default de zeroOpenState descarta TODOS los cruces en un
	// vehículo cableado al revés.
	cfgInicial := base
	if !disablePlatformConfig {
		switch cached, err := parametros.LeerCache(platformConfigCache); {
		case err != nil:
			warnlog.Printf("config: el cache %q no se pudo usar: %s", platformConfigCache, err)
		case cached != nil:
			cfgInicial = client.Resolver(base, cached, fijados)
			infolog.Printf("config: cache de plataforma aplicado (guardado %s)",
				parametros.Marca(platformConfigCache))
		default:
			infolog.Printf("config: todavía no hay cache de plataforma en %q", platformConfigCache)
		}
	}

	counting := client.NewCountingActor()
	counting.SetLogError(errlog).SetLogWarn(warnlog).SetLogInfo(infolog).
		SetLogBuild(buildlog)
	if debug {
		counting.WithDebug()
	}
	// SetConfig fija la configuración inicial y de paso el gating de cada puerta y la
	// dirección del keep-alive. El keep-alive consulta la primera cámara configurada; sin
	// cámaras queda la dirección histórica, para no alterar los equipos ya desplegados.
	counting.SetConfig(cfgInicial, fijados)
	counting.SetVideoFactory(videoFactory(camUser, camPass, videoMinFree))
	counting.SetCameraFactory(cameraFactory(camUser, camPass, videoMinFree))
	counting.SetWithoutEventID(withoutEventID)
	// Se avisa en WARN y no en INFO porque es una regresión deliberada del formato: el
	// paso se publica sin la llave que lo une con su video, y quien lea el log meses
	// después tiene que poder ver que el binario corría capado a propósito.
	if withoutEventID {
		warnlog.Println("-withoutEventID: el COUNTERSDOOR sale SIN event_id, formato previo " +
			"a 1.0.32. La plataforma no va a poder cruzar el paso con su video. " +
			"Quitá el flag cuando la plataforma acepte el campo")
	}

	// Apagado de la cámara comandado por go-ignition. La línea se deja en el nivel de
	// "encendida" en una sola operación de sysfs, así que al arrancar el binario la
	// cámara no parpadea.
	//
	// Un fallo acá DETIENE el arranque, igual que un -videoCodec mal escrito. Seguir
	// sin control de energía dejaría la configuración diciendo una cosa y el equipo
	// haciendo otra, y eso se descubre cuando la batería ya se gastó.
	if cfgInicial.CameraPowerGPIO > 0 {
		nivelEncendido := 1
		if cfgInicial.CameraPowerOffLevel == 1 {
			nivelEncendido = 0
		}
		salida, err := client.NuevaSalidaGPIO(cfgInicial.CameraPowerGPIO, nivelEncendido)
		switch {
		case errors.Is(err, client.ErrNivelNoVerificado):
			// La línea se maneja pero lee distinto de lo que se le pidió. Es lo que
			// pasa con el pad todavía sin cablear, así que NO detiene el arranque: el
			// conteo de pasajeros no se negocia por el cableado de un accesorio.
			warnlog.Printf("cameraPowerGPIO: %s; se continúa, pero hasta confirmarlo "+
				"con el relé conectado no se puede dar por hecho que la cámara se apague", err)
		case err != nil && fijados[client.FlagCameraPowerGPIO]:
			// El número lo escribió quien lanzó el binario: detenerse es correcto, porque
			// la configuración dice una cosa y el equipo hace otra, y eso se descubre
			// cuando la batería ya se gastó.
			log.Fatalf("-cameraPowerGPIO: %s", err)
		case err != nil:
			// El número vino de la PLATAFORMA (o del cache). Acá detenerse sería mucho
			// peor: un valor mal cargado dejaría a toda la flota sin arrancar, en bucle
			// de supervisión, y no se podría corregir desde el lanzador —habría que
			// entrar a cada equipo a borrar el cache—. Se deshabilita solo el apagado y
			// se cuenta igual, que es lo que no se negocia.
			errlog.Printf("cameraPowerGPIO %d (de plataforma): %s; se deshabilita el "+
				"apagado de la cámara y el conteo sigue", cfgInicial.CameraPowerGPIO, err)
			salida = nil
		}
		if salida != nil {
			counting.SetCameraPower(salida, cfgInicial.CameraPowerOffLevel,
				cameraPowerOffDelay, cameraBootDelay)
			infolog.Printf("apagado de camara por gpio %d habilitado: apagada=%d, encendida=%d, "+
				"corte tras %v, arranque %v", cfgInicial.CameraPowerGPIO,
				cfgInicial.CameraPowerOffLevel, nivelEncendido, cameraPowerOffDelay, cameraBootDelay)
		}
	}

	// El banner dice, parámetro por parámetro, de dónde salió el valor. No es cosmético:
	// con la prioridad invertida, un flag en el lanzador deja ese parámetro fuera del
	// alcance de la plataforma, y sin esta línea eso se descubre recién cuando alguien lo
	// cambia desde la plataforma y el equipo no le hace caso.
	infolog.Printf("configuración efectiva al arrancar:\n%s", cfgInicial.Describir(fijados))

	propsCounting := actor.PropsFromProducer(func() actor.Actor { return counting }, actor.WithReceiverMiddleware(persistence.Using(provider)))
	pidCounting, err := rootContext.SpawnNamed(propsCounting, "counting")
	if err != nil {
		time.Sleep(3 * time.Second)
		errlog.Panicln(err)
	}

	listenner := client.NewListen(socket, pidCounting)
	listenner.SetCameras(cfgInicial.Camaras)
	listenner.SetLogError(errlog).SetLogWarn(warnlog).
		SetLogInfo(infolog).SetLogBuild(buildlog).SetLogCamera(cameralog)

	if debug {
		listenner.WithDebug()
	}

	propsListen := actor.PropsFromFunc(listenner.Receive)
	pidListen, err := rootContext.SpawnNamed(propsListen, "listenner")
	if err != nil {
		time.Sleep(3 * time.Second)
		errlog.Panicln(err)
	}

	time.Sleep(1 * time.Second)

	rootContext.Send(pidListen, &messages.CountingActor{
		Address: pidCounting.Address,
		ID:      pidCounting.Id})

	// counting necesita el PID de listenner para pasarle la lista de cámaras cuando la
	// plataforma la cambie. Va en este sentido porque el orden de arranque es el inverso:
	// listenner se construye CON el PID de counting, así que counting no puede conocerlo
	// antes.
	rootContext.Send(pidCounting, &client.MsgRegistrarListen{PID: pidListen})

	// La configuración de plataforma se levanta al final, cuando el árbol ya está en pie:
	// lo primero que hace es mandar un MsgAplicarConfig, y no tendría a quién.
	if disablePlatformConfig {
		infolog.Println("config: -disablePlatformConfig, no se descarga configuración de plataforma")
	} else {
		arrancarParametros(sistema, base, fijados, pidCounting)
	}

	time.Sleep(3 * time.Second)

	rootContext.Send(pidCounting, &client.MsgSendRegisters{})

	infolog.Printf("back camera counter START --  version: %s\n", showVersion)

	go func() {
		t1 := time.NewTicker(45 * time.Second)
		defer t1.Stop()
		for range t1.C {
			rootContext.Send(pidCounting, &client.MsgSendRegisters{})
		}
	}()

	finish := make(chan os.Signal, 1)
	signal.Notify(finish, syscall.SIGINT)
	signal.Notify(finish, syscall.SIGTERM)
	<-finish
}
