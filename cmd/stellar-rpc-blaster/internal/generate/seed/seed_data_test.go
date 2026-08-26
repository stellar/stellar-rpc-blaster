package seed

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
)

func TestAddEventDataDedupThenTrim(t *testing.T) {
	c := ContractEvents{ContractIds: map[string]*TopicData{}}
	c.AddEventData(stubEvent(0, "C0", "t1", "p1"))
	c.AddEventData(stubEvent(0, "C0", "t1", "p1")) // duplicate params: counted, not stored twice
	c.AddEventData(stubEvent(0, "C0", "t1", "p2"))
	c.AddEventData(protocol.EventInfo{ContractID: "C0", TopicXDR: nil}) // zero-topic: ignored

	td := c.ContractIds["C0"]
	require.EqualValues(t, 3, td.Count)
	require.EqualValues(t, 3, td.Topic["t1"].Count)
	require.Len(t, td.Topic["t1"].Params, 2)

	for i := 1; i < 30; i++ {
		id := fmt.Sprintf("C%02d", i)
		for range i + 1 {
			c.AddEventData(stubEvent(0, id, "t"))
		}
	}
	c.trim(3)
	require.Len(t, c.ContractIds, 3)
	for _, id := range []string{"C29", "C28", "C27"} {
		require.Contains(t, c.ContractIds, id)
	}
}
