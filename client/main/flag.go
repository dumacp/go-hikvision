package main

import (
	"fmt"
	"strings"
)

type zeroFlags []bool

func (i *zeroFlags) String() string {
	return fmt.Sprintf("%v", *i)
}

func (i *zeroFlags) Set(value string) error {
	if len(value) > 0 && strings.ToUpper(value) == "TRUE" {
		*i = append(*i, true)
	} else {
		*i = append(*i, false)
	}
	return nil
}

// cameraFlags collects the -camera occurrences: the n-th one configures the camera of
// door n-1, the same positional convention as -zeroOpenState and -countWithCloseDoor.
type cameraFlags []string

func (i *cameraFlags) String() string {
	return fmt.Sprintf("%v", *i)
}

func (i *cameraFlags) Set(value string) error {
	*i = append(*i, strings.TrimSpace(value))
	return nil
}

type closeFlags []bool

func (i *closeFlags) String() string {
	return fmt.Sprintf("%v", *i)
}

func (i *closeFlags) Set(value string) error {
	if len(value) > 0 && strings.ToUpper(value) == "TRUE" {
		*i = append(*i, true)
	} else {
		*i = append(*i, false)
	}
	return nil
}
