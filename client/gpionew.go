package client

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/mkch/gpio"
)

type GpioValue struct {
	Pin   uint32
	Value uint
}

type GpioEvents struct {
	pin  uint32
	ch   chan *GpioValue
	quit chan int
}

func NewGpioEvent(pin uint32) *GpioEvents {
	return &GpioEvents{pin: pin}
}

func (e *GpioEvents) Close() error {

	if e.quit != nil {
		select {
		case _, ok := <-e.quit:
			if ok {
				close(e.quit)
			}
		default:
			close(e.ch)
		}
	}
	if e.ch == nil {
		return fmt.Errorf("channel is nil")
	}

	return nil
}

func (e *GpioEvents) Events() (<-chan *GpioValue, error) {

	e.Close()

	handleFlags := gpio.Input
	eventsFlags := gpio.BothEdges

	chip1, err := gpio.OpenChip(fmt.Sprintf("gpiochip%d", e.pin/32))
	if err != nil {
		return nil, fmt.Errorf("OpenChip (%d) err: %w", e.pin, err)
	}
	evts1, err := chip1.OpenLineWithEvents(e.pin%32, handleFlags, eventsFlags, fmt.Sprintf("go-doors-%d", e.pin))
	if err != nil {
		return nil, fmt.Errorf("OpenLineWithEvents (%d-%d) err: %w", e.pin/32, e.pin%32, err)
	}
	chPuerta1 := evts1.Events()
	value1 := func() uint {
		if v, err := evts1.Value(); err != nil {
			log.Println(err)
			return 0
		} else {
			return uint(v)
		}
	}()

	lastValue1 := uint(127)

	e.ch = make(chan *GpioValue)
	ch := e.ch

	go func() {
		defer close(ch)
		defer evts1.Close()

		chSending1 := time.NewTimer(1 * time.Second)

		defer chSending1.Stop()

		for {
			select {
			case <-e.quit:
				return
			case <-chSending1.C:
				if lastValue1 != value1 {
					ch <- &GpioValue{e.pin, value1}
					lastValue1 = value1
				}
			case v := <-chPuerta1:
				fmt.Printf("evt: %v\n", v)
				if v.RisingEdge {
					value1 = 1
				} else {
					value1 = 0
				}
				if !chSending1.Stop() {
					select {
					case <-chSending1.C:
					default:
					}
				}
				chSending1.Reset(1 * time.Second)
			}
		}
	}()

	return ch, nil
}

func listDevice(deviceName string) (err error) {
	chip, err := gpio.OpenChip(deviceName)
	if err != nil {
		return
	}
	// Inspect this GPIO chip
	chipInfo, err := chip.Info()
	if err != nil {
		return
	}
	fmt.Printf("GPIO chip: %v, \"%v\", %v GPIO lines\n", chipInfo.Name, chipInfo.Label, chipInfo.NumLines)
	// Loop over the lines and print info
	for i := uint32(0); i < chipInfo.NumLines; i++ {
		var lineInfo gpio.LineInfo
		lineInfo, err = chip.LineInfo(i)
		if err != nil {
			return
		}
		fmt.Printf("\tline %2d:", lineInfo.Offset)
		if len(lineInfo.Name) > 0 {
			fmt.Printf(` "%v"`, lineInfo.Name)
		} else {
			fmt.Print(" unnamed")
		}
		if len(lineInfo.Consumer) > 0 {
			fmt.Printf(` "%v"`, lineInfo.Consumer)
		} else {
			fmt.Print(" unused")
		}
		var flags []string
		if lineInfo.Kernel() {
			flags = append(flags, "kernel")
		}
		if lineInfo.Output() {
			flags = append(flags, "output")
		}
		if lineInfo.ActiveLow() {
			flags = append(flags, "active-low")
		}
		if lineInfo.OpenDrain() {
			flags = append(flags, "open-drain")
		}
		if lineInfo.OpenSource() {
			flags = append(flags, "open-source")
		}
		fmt.Printf(" [%v]\n", strings.Join(flags, " "))
	}
	return
}
