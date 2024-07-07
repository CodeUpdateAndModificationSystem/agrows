//go:build exclude

package agrows

import (
	"fmt"

	log "github.com/sett17/dnutlogger"
)

const (
	cool = "cool"
)

func DebugShit(index int) (string, error) {
	log.Info("DebugShit")
	return fmt.Sprintf("krank %d", index), nil
}

type MyType struct {
	cool      string
	something byte
}

// Comments stay!
func Whatever(prefix string, my MyType) {
	log.Info("Whatever")
}

func thisIsNottouched() {
	panic("this is not touched")
}

func NoParamsFunc() error {
	return nil
}

func NamedReturns(eyjo complex128) (text string, err error) {
	return
}
