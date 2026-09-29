package main

import (
	"sync"
	"testing"
	"time"
)

func TestBackgroundTasksWaitForEveryWorker(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	wait := startBackgroundTasks(
		func() {
			started <- struct{}{}
			<-release
		},
		func() {
			started <- struct{}{}
			<-release
		},
	)
	defer func() {
		releaseOnce.Do(func() { close(release) })
		wait()
	}()

	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("background worker did not start")
		}
	}

	waitDone := make(chan struct{})
	go func() {
		wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
		t.Fatal("wait returned before workers finished")
	case <-time.After(25 * time.Millisecond):
	}

	releaseOnce.Do(func() { close(release) })
	select {
	case <-waitDone:
	case <-time.After(time.Second):
		t.Fatal("wait did not return after workers finished")
	}
}
