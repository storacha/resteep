package main

import (
	"encoding/binary"
	"fmt"
	"log"
	"time"

	"github.com/storacha/resteep"
)

func main() {
	err := resteep.Resteep(
		func(state []byte, stateCh chan<- []byte) error {
			num := 0

			if len(state) == 4 {
				num = int(binary.BigEndian.Uint32(state))
			}

			for {
				fmt.Printf("\rCurrent state: %d", num)
				num++
				buf := make([]byte, 4)
				binary.BigEndian.PutUint32(buf, uint32(num))
				stateCh <- buf
				// Wait 1s
				time.Sleep(1 * time.Second)
			}
		},
	)
	if err != nil {
		log.Fatalln(err)
	}
}
