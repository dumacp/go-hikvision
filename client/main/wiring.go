package main

import (
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/dumacp/go-hikvision/client"
)

// Fábricas de los dos actores cuya configuración puede cambiar en caliente.
//
// Antes estas Props se armaban una sola vez en main con los valores de los flags, y eso
// dejaba la configuración congelada: cambiar el codec o el directorio de video exigía
// reiniciar el proceso. Ahora CountingActor las pide cada vez que necesita (re)crear el
// hijo, con la configuración vigente.
//
// Devolver nil significa "este actor no va". Es como se apaga la extracción de video o el
// mantenimiento de cámara, y es exactamente el mismo camino que ya existía cuando faltaba
// -videoDir o las credenciales: un solo lugar donde se decide.

// videoFactory arma el VideoActor. La extracción necesita las tres cosas: dónde guardar, a
// qué cámara preguntarle y con qué credenciales.
func videoFactory(user, pass string, minFree uint64) func(client.Efectiva) *actor.Props {
	return func(cfg client.Efectiva) *actor.Props {
		switch {
		case len(cfg.VideoDir) == 0:
			infolog.Println("video extraction disabled (no video directory configured)")
			return nil
		case !cfg.HayCamaras():
			warnlog.Println("video extraction disabled: there is no camera configured")
			return nil
		case len(user) == 0:
			warnlog.Printf("video extraction disabled: %s is not usable", envCredentials)
			return nil
		}
		vid := client.NewVideoActor(cfg.Camaras, user, pass, cfg.VideoDir,
			videoPreRoll, videoDuration, videoQueue, minFree)
		vid.SetLogError(errlog).SetLogWarn(warnlog).SetLogInfo(infolog).SetLogBuild(buildlog)
		if debug {
			vid.WithDebug()
		}
		infolog.Printf("video extraction enabled: dir=%q preRoll=%v duration=%v queue=%d minFree=%d MB",
			cfg.VideoDir, videoPreRoll, videoDuration, videoQueue, minFree>>20)
		// Vale avisarlo fuerte: sin piso, la extracción llena la partición hasta el final,
		// y ahí vive la boltdb del conteo. Se pierde el acumulado del vehículo, que es
		// mucho más caro que perder unos clips.
		if minFree == 0 {
			warnlog.Printf("-videoMinFree 0: SIN piso de disco. La extracción va a llenar %q "+
				"hasta el último byte, y en esa partición está la base del conteo", cfg.VideoDir)
		}
		return actor.PropsFromProducer(func() actor.Actor { return vid })
	}
}

// cameraFactory arma el CameraActor: hora y NTP, horario de grabación, salud del
// almacenamiento y perfil de codificación.
//
// Se crea si se pide CUALQUIERA de sus trabajos: el mantenimiento de hora (-ntpServer) o el
// perfil de codificación. Adentro cada bloque se autolimita: sin -ntpServer no se toca el
// reloj ni los servidores NTP —la deriva se mide igual, para el reporte— pero el horario de
// grabación sí se alinea, porque sin grabación no hay video que extraer.
func cameraFactory(user, pass string, minFree uint64) func(client.Efectiva) *actor.Props {
	return func(cfg client.Efectiva) *actor.Props {
		pideEncoder := len(cfg.SmartCodec) > 0 || len(cfg.VideoCodec) > 0 ||
			cfg.VideoFrameRate > 0 || videoGop > 0 || videoQuality > 0 || videoBitrateMax > 0
		switch {
		case len(cfg.NtpServer) == 0 && !pideEncoder:
			infolog.Println("camera maintenance disabled (neither NTP server nor an encoder setting)")
			return nil
		case !cfg.HayCamaras():
			warnlog.Println("camera maintenance disabled: there is no camera configured")
			return nil
		case len(user) == 0:
			warnlog.Printf("camera maintenance disabled: %s is not usable", envCredentials)
			return nil
		}
		cam := client.NewCameraActor(cfg.Camaras, user, pass, client.CameraConfig{
			Server:           cfg.NtpServer,
			Port:             ntpPort,
			Interval:         ntpInterval,
			TimeZone:         ntpTimeZone,
			DriftMax:         ntpDriftMax,
			RecordStart:      recordStart,
			RecordEnd:        recordEnd,
			SmartCodec:       cfg.SmartCodec,
			FrameRate:        cfg.VideoFrameRate,
			GopFrames:        videoGop,
			VideoCodec:       cfg.VideoCodec,
			Quality:          videoQuality,
			BitrateMax:       videoBitrateMax,
			EncoderMinUptime: encoderMinUptime,
		}, cameraCheckInterval, cfg.VideoDir, minFree)
		cam.SetLogError(errlog).SetLogWarn(warnlog).SetLogInfo(infolog).SetLogBuild(buildlog)
		if debug {
			cam.WithDebug()
		}
		if len(cfg.NtpServer) > 0 {
			infolog.Printf("camera time maintenance enabled: ntp=%s:%d every %v, zone=%q, driftMax=%v, check=%v, recording %s-%s",
				cfg.NtpServer, ntpPort, ntpInterval, ntpTimeZone, ntpDriftMax, cameraCheckInterval,
				recordStart, recordEnd)
		} else {
			infolog.Printf("camera maintenance enabled without NTP: no se toca el reloj; "+
				"check=%v, recording %s-%s", cameraCheckInterval, recordStart, recordEnd)
		}
		// El perfil del encoder se anuncia aparte porque es el único que puede terminar en
		// un reinicio de la cámara, y conviene verlo en el log.
		if pideEncoder {
			infolog.Printf("encoder profile: codec=%q smartCodec=%q fps=%d gop=%d quality=%d "+
				"bitrateMax=%d; se aplica a los %v de encendido, y reinicia la cámara si el "+
				"cambio lo exige",
				cfg.VideoCodec, cfg.SmartCodec, cfg.VideoFrameRate, videoGop, videoQuality,
				videoBitrateMax, encoderMinUptime)
			// El aviso no es formal: bajar este mínimo es exactamente lo que permite que un
			// binario en bucle de supervisión reinicie cámaras una y otra vez.
			if encoderMinUptime < 5*time.Minute {
				warnlog.Printf("-encoderMinUptime en %v: si el binario entra en bucle de "+
					"reinicios va a alcanzar ese umbral en cada vuelta y puede reiniciar la "+
					"cámara repetidas veces. El default de %v existe para evitarlo",
					encoderMinUptime, client.EncoderMinUptimeDefault)
			}
		}
		return actor.PropsFromProducer(func() actor.Actor { return cam })
	}
}
