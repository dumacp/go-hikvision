package client

import (
	"fmt"
	"time"

	"github.com/brian-armstrong/gpio"
)

func gpioNewWatcher(quit chan int, pins ...uint) (ch chan [2]uint, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("Recovered in GPIO: %s", r)
		}
	}()

	gpioPins := make(map[uint]gpio.Pin, 0)
	gpioMems := make(map[uint]uint, 0)
	for _, pin := range pins {
		gpioPins[pin] = gpio.NewInput(pin)
		gpioMems[pin] = 100
	}

	ch = make(chan [2]uint, 0)
	go func() {
		defer close(ch)
		t1 := time.NewTicker(3 * time.Second)
		defer t1.Stop()
		for {
			select {
			case <-quit:
				return
			case <-t1.C:
				for pin, v := range gpioPins {
					value, err := v.Read()
					if err != nil {
						fmt.Println(err)
						break
					}
					// fmt.Printf("read %d from gpio %d\n", value, pin)
					memValue, ok := gpioMems[pin]
					if !ok {
						break
					}
					if memValue != value {
						select {
						case ch <- [2]uint{uint(pin), value}:
						case <-time.After(1 * time.Second):
						}
						gpioMems[pin] = value
					}
				}
			}
		}
	}()
	return ch, nil
}
