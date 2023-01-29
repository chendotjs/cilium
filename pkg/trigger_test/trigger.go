package main

import (
	"fmt"
	"log"
	"time"

	"github.com/cilium/cilium/pkg/trigger"
)

func f(reasons []string) {
	log.Println("emmm, triggered:", reasons, "len:", len(reasons))
}

func main() {
	t, err := trigger.NewTrigger(trigger.Parameters{
		Name:        "crd-allocator-node-refresher",
		MinInterval: time.Second * 3,
		TriggerFunc: f,
	})
	if err != nil {
		log.Fatal("NewTrigger", err)
	}

	go func() {
		var (
			i = 0
		)

		t.TriggerWithReason("init")
		time.Sleep(time.Millisecond * 1)

		for {
			reason := fmt.Sprintf("emit %d", i)
			t.TriggerWithReason(reason)
			log.Println("trigger", reason)
			time.Sleep(time.Millisecond * 100)

			i++
		}
	}()

	var (
		ch chan struct{} = make(chan struct{}, 1)
	)

	<-ch
}
