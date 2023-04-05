package client

import "encoding/json"

type registerMap struct {
	Inputs0    int64 `json:"inputs0"`
	Inputs1    int64 `json:"inputs1"`
	Outputs0   int64 `json:"outputs0"`
	Outputs1   int64 `json:"outputs1"`
	Anomalies0 int64 `json:"anomalies0"`
	Anomalies1 int64 `json:"anomalies1"`
	Tampering0 int64 `json:"tampering0"`
	Tampering1 int64 `json:"tampering1"`
}

func registersMap(inputs, outputs, anomalies, tampering map[int32]int64) ([]byte, error) {
	reg := &registerMap{}

	reg.Inputs0 = inputs[0]
	reg.Outputs0 = outputs[0]
	reg.Anomalies0 = anomalies[0]
	reg.Tampering0 = tampering[0]
	reg.Inputs1 = inputs[1]
	reg.Outputs1 = outputs[1]
	reg.Anomalies1 = anomalies[1]
	reg.Tampering1 = tampering[1]

	if v, err := json.Marshal(reg); err != nil {
		return nil, err
	} else {
		return v, nil
	}
}

func verifySum(data map[int32]int64) int64 {
	sum := int64(0)
	for _, v := range data {
		sum += v
	}
	return sum
}

func Abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
