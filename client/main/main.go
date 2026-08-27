package main

import (
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
	"golang.org/x/exp/errors/fmt"
)

const (
	showVersion = "1.0.37"
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
var videoQueue int

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

var isZeroOpenState zeroFlags
var enableCountWithCloseDoor closeFlags
var cameras cameraFlags

func init() {
	flag.BoolVar(&debug, "debug", false, "debug enable")
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
	flag.DurationVar(&videoDuration, "videoDuration", 16*time.Second, "clip duration")
	flag.IntVar(&videoQueue, "videoQueue", 500,
		"maximum pending extractions; playback runs in real time, so a burst queues up")
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

	rootContext := actor.NewActorSystem().Root

	if len(isZeroOpenState) <= 0 {
		isZeroOpenState = []bool{false}
	}
	if len(enableCountWithCloseDoor) <= 0 {
		enableCountWithCloseDoor = []bool{false}
	}

	fmt.Printf("zeroOpenState: %s\n", describeDoorBool(isZeroOpenState))
	fmt.Printf("countWithCloseDoor: %s\n", describeDoorBool(enableCountWithCloseDoor))
	if len(camerasByDoor) > 0 {
		fmt.Printf("cameras: %s\n", describeCameras(camerasByDoor))
	} else {
		fmt.Printf("cameras: not configured, using the legacy rule (%s -> door 1, rest -> door 0)\n",
			client.LegacyBackDoorIP())
	}

	counting := client.NewCountingActor()
	applyDoorBool(isZeroOpenState, counting.SetZeroOpenState)
	applyDoorBool(enableCountWithCloseDoor, counting.SetCountCloseDoor)
	counting.SetLogError(errlog).SetLogWarn(warnlog).SetLogInfo(infolog).
		SetLogBuild(buildlog)
	if debug {
		counting.WithDebug()
	}

	// La extracción de video necesita las tres cosas: dónde guardar, a qué cámara
	// preguntarle y con qué credenciales. Si falta alguna, queda deshabilitada y el
	// conteo sigue igual.
	switch {
	case len(videoDir) == 0:
		infolog.Println("video extraction disabled (-videoDir not set)")
	case len(camerasByDoor) == 0:
		warnlog.Println("video extraction disabled: -videoDir is set but there is no -camera")
	case len(camUser) == 0:
		warnlog.Printf("video extraction disabled: -videoDir is set but %s is not usable", envCredentials)
	default:
		vid := client.NewVideoActor(camerasByDoor, camUser, camPass, videoDir,
			videoPreRoll, videoDuration, videoQueue)
		vid.SetLogError(errlog).SetLogWarn(warnlog).SetLogInfo(infolog).SetLogBuild(buildlog)
		if debug {
			vid.WithDebug()
		}
		counting.SetVideoProps(actor.PropsFromProducer(func() actor.Actor { return vid }))
		infolog.Printf("video extraction enabled: dir=%q preRoll=%v duration=%v queue=%d",
			videoDir, videoPreRoll, videoDuration, videoQueue)
	}

	// El actor de cámara se crea si se pide CUALQUIERA de sus trabajos: el mantenimiento de
	// hora (-ntpServer) o el perfil de codificación. Antes solo lo creaba -ntpServer, así que
	// un -videoCodec o un -smartCodec sin él quedaban ignorados en silencio, con la única
	// pista de un "camera time maintenance disabled" que hablaba de otra cosa.
	//
	// Adentro cada bloque se autolimita: sin -ntpServer no se toca el reloj ni los servidores
	// NTP, aunque la deriva se sigue midiendo para el reporte. El horario de grabación sí se
	// alinea en los dos casos, porque sin grabación no hay video que extraer y quien configura
	// el codec está justamente haciendo video.
	pideEncoder := len(smartCodec) > 0 || len(videoCodec) > 0 ||
		videoFrameRate > 0 || videoGop > 0 || videoQuality > 0 || videoBitrateMax > 0
	switch {
	case len(ntpServer) == 0 && !pideEncoder:
		infolog.Println("camera maintenance disabled (neither -ntpServer nor an encoder flag set)")
	case len(camerasByDoor) == 0:
		warnlog.Println("camera maintenance disabled: there is no -camera")
	case len(camUser) == 0:
		warnlog.Printf("camera maintenance disabled: %s is not usable", envCredentials)
	default:
		cam := client.NewCameraActor(camerasByDoor, camUser, camPass, client.CameraConfig{
			Server:      ntpServer,
			Port:        ntpPort,
			Interval:    ntpInterval,
			TimeZone:    ntpTimeZone,
			DriftMax:    ntpDriftMax,
			RecordStart: recordStart,
			RecordEnd:   recordEnd,
			SmartCodec:  smartCodec,
			FrameRate:   videoFrameRate,
			GopFrames:   videoGop,
			VideoCodec:  videoCodec,
			Quality:     videoQuality,
			BitrateMax:  videoBitrateMax,
		}, cameraCheckInterval)
		cam.SetLogError(errlog).SetLogWarn(warnlog).SetLogInfo(infolog).SetLogBuild(buildlog)
		if debug {
			cam.WithDebug()
		}
		counting.SetCameraProps(actor.PropsFromProducer(func() actor.Actor { return cam }))
		if len(ntpServer) > 0 {
			infolog.Printf("camera time maintenance enabled: ntp=%s:%d every %v, zone=%q, driftMax=%v, check=%v, recording %s-%s",
				ntpServer, ntpPort, ntpInterval, ntpTimeZone, ntpDriftMax, cameraCheckInterval,
				recordStart, recordEnd)
		} else {
			infolog.Printf("camera maintenance enabled without NTP: no se toca el reloj; "+
				"check=%v, recording %s-%s", cameraCheckInterval, recordStart, recordEnd)
		}
		// El perfil del encoder se anuncia aparte porque es el único que puede terminar en
		// un reinicio de la cámara, y conviene verlo en el log de arranque.
		if pideEncoder {
			infolog.Printf("encoder profile: codec=%q smartCodec=%q fps=%d gop=%d quality=%d "+
				"bitrateMax=%d; se aplica cuando el binario lleve un rato encendido, y "+
				"reinicia la cámara si el cambio lo exige",
				videoCodec, smartCodec, videoFrameRate, videoGop, videoQuality, videoBitrateMax)
		}
	}

	propsCounting := actor.PropsFromProducer(func() actor.Actor { return counting }, actor.WithReceiverMiddleware(persistence.Using(provider)))
	pidCounting, err := rootContext.SpawnNamed(propsCounting, "counting")
	if err != nil {
		time.Sleep(3 * time.Second)
		errlog.Panicln(err)
	}

	listenner := client.NewListen(socket, pidCounting)
	listenner.SetCameras(camerasByDoor)
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
