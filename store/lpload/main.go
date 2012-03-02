package main

import (
	"fmt"
	"launchpad.net/juju/go/store"
	"launchpad.net/juju/go/log"
	"launchpad.net/lpad"
	stdlog "log"
	"os"
)

func main() {
	log.Target = stdlog.New(os.Stdout, "", stdlog.LstdFlags)
	err := load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func load() error {
	if len(os.Args) != 2 || len(os.Args[1]) == 0 || os.Args[1][0] == '-' {
		return fmt.Errorf("usage: lpload <mongo addr>")
	}
	s, err := store.Open(os.Args[1])
	if err != nil {
		return err
	}
	defer s.Close()
	err = store.PublishCharmsDistro(s, lpad.Production)
	if _, ok := err.(store.PublishBranchErrors); !ok {
		// Ignore branch errors since they're commonplace here.
		// They're logged, though.
		return err
	}
	return nil
}
