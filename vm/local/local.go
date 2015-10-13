// Copyright 2015 syzkaller project authors. All rights reserved.
// Use of this source code is governed by Apache 2 LICENSE that can be found in the LICENSE file.

package qemu

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/google/syzkaller/vm"
)

func init() {
	vm.Register("local", ctor)
}

type local struct {
	params
	workdir  string
	syscalls map[int]bool
	id       int
	mgrPort  int
}

type params struct {
	Fuzzer   string
	Executor string
	Parallel int
}

func ctor(workdir string, syscalls map[int]bool, port, index int, paramsData []byte) (vm.Instance, error) {
	p := new(params)
	if err := json.Unmarshal(paramsData, p); err != nil {
		return nil, fmt.Errorf("failed to unmarshal local params: %v", err)
	}
	if _, err := os.Stat(p.Fuzzer); err != nil {
		return nil, fmt.Errorf("fuzzer binary '%v' does not exist: %v", p.Fuzzer, err)
	}
	if _, err := os.Stat(p.Executor); err != nil {
		return nil, fmt.Errorf("executor binary '%v' does not exist: %v", p.Executor, err)
	}
	if p.Parallel == 0 {
		p.Parallel = 1
	}
	if p.Parallel <= 0 || p.Parallel > 100 {
		return nil, fmt.Errorf("bad parallel param: %v, want [1-100]", p.Parallel)
	}

	os.MkdirAll(workdir, 0770)

	// Disable annoying segfault dmesg messages, fuzzer is going to crash a lot.
	etrace, err := os.Open("/proc/sys/debug/exception-trace")
	if err == nil {
		etrace.Write([]byte{'0'})
		etrace.Close()
	}

	// Don't write executor core files.
	syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{0, 0})

	loc := &local{
		params:   *p,
		workdir:  workdir,
		syscalls: syscalls,
		id:       index,
		mgrPort:  port,
	}
	return loc, nil
}

func (loc *local) Run() {
	name := fmt.Sprintf("local-%v", loc.id)
	log.Printf("%v: started\n", name)
	for run := 0; ; run++ {
		cmd := exec.Command(loc.Fuzzer, "-name", name, "-saveprog", "-executor", loc.Executor,
			"-manager", fmt.Sprintf("localhost:%v", loc.mgrPort), "-parallel", fmt.Sprintf("%v", loc.Parallel))
		if len(loc.syscalls) != 0 {
			buf := new(bytes.Buffer)
			for c := range loc.syscalls {
				fmt.Fprintf(buf, ",%v", c)
			}
			cmd.Args = append(cmd.Args, "-calls="+buf.String()[1:])
		}
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Dir = loc.workdir
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			log.Printf("failed to start fuzzer binary: %v", err)
			time.Sleep(10 * time.Second)
			continue
		}
		pid := cmd.Process.Pid
		done := make(chan bool)
		go func() {
			select {
			case <-done:
			case <-time.After(time.Hour):
				log.Printf("%v: running for long enough, restarting", name)
				syscall.Kill(-pid, syscall.SIGKILL)
				syscall.Kill(-pid, syscall.SIGKILL)
				syscall.Kill(pid, syscall.SIGKILL)
				syscall.Kill(pid, syscall.SIGKILL)
			}
		}()
		err := cmd.Wait()
		close(done)
		log.Printf("fuzzer binary exited: %v", err)
		time.Sleep(10 * time.Second)
	}
}