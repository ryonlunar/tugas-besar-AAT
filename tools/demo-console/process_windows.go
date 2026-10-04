package main

import "os/exec"

// Native Windows can compile the package; run this operator tool inside WSL
// so subprocess interruption can execute the Python trials' finally blocks.
func prepareProcess(cmd *exec.Cmd) {}
