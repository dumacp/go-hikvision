package client

import (
	"fmt"
	"sync"
)

type MapInterface interface {
	Add(key int32, value int64)
	Get(key int32) int64
	Delete(key int32)
	Iterate() map[int32]int64
}

func NewMyMap() MapInterface {
	return &MyMap{myMap: make(map[int32]int64)}
}

type MyMap struct {
	mu    sync.Mutex
	myMap map[int32]int64
}

func (m *MyMap) String() string {
	return fmt.Sprintf("%v", m.Iterate())
}

func (m *MyMap) Add(key int32, value int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.myMap[key] = value
}

func (m *MyMap) Get(key int32) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.myMap[key]
}

func (m *MyMap) Delete(key int32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.myMap, key)
}

func (m *MyMap) Iterate() map[int32]int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[int32]int64)
	for k, v := range m.myMap {
		result[k] = v
	}
	return result
}
