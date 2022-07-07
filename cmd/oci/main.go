package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/jessevdk/go-flags"
	"github.com/oracle/oci-go-sdk/common"
	"github.com/oracle/oci-go-sdk/common/auth"
	"github.com/oracle/oci-go-sdk/loggingingestion"
	"github.com/wilsonzlin/journald-exporter/pkg/runner"
)

func main() {
	var args struct {
		LogOcid      string `long:"log-ocid"`
		InstanceOcid string `long:"instance-ocid"`
		StateDir     string `long:"state-dir"`
	}
	_, err := flags.ParseArgs(&args, os.Args[1:])
	if err != nil {
		panic(err)
	}

	provider, err := auth.InstancePrincipalConfigurationProvider()
	if err != nil {
		panic(err)
	}
	client, err := loggingingestion.NewLoggingClientWithConfigurationProvider(provider)

	var mutex sync.Mutex
	var entriesBatch []loggingingestion.LogEntry

	go func() {
		// Don't delay too long as it could cause growing backlog if logs are on fire.
		MIN_DELAY := 2 * time.Second
		MAX_DELAY := 1 * time.Minute
		delay := MIN_DELAY
		for {
			time.Sleep(delay)

			mutex.Lock()
			entryCount := 0
			contentLength := 0
			for _, e := range entriesBatch {
				// Approximate byte count of other parts of the JSON object as 512 bytes.
				entryByteSize := len(*e.Data) + 512
				// The PutLogs API has a Content-Length limit of 11 MiB.
				if contentLength+entryByteSize > 11534336 {
					break
				}
				entryCount++
				contentLength += entryByteSize
			}
			mutex.Unlock()
			if entryCount == 0 {
				delay = MIN_DELAY
				continue
			}
			req := loggingingestion.PutLogsRequest{
				PutLogsDetails: loggingingestion.PutLogsDetails{
					LogEntryBatches: []loggingingestion.LogEntryBatch{
						{
							Defaultlogentrytime: &common.SDKTime{Time: time.Now()},
							Entries:             entriesBatch[:entryCount],
							Source:              common.String(args.InstanceOcid),
							Type:                common.String("journald"),
						},
					},
					Specversion: common.String("1.0"),
				},
				LogId: common.String(args.LogOcid),
			}

			// Send the request using the service client
			_, err = client.PutLogs(context.Background(), req)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to PutLogs %d entries: %s\n", entryCount, err)
				delay = delay * 2
				if delay > MAX_DELAY {
					delay = MAX_DELAY
				}
			} else {
				delay = MIN_DELAY
				if args.StateDir != "" {
					err := os.WriteFile(fmt.Sprintf("%s/after.cursor.tmp", args.StateDir), []byte(*entriesBatch[entryCount-1].Id), 0o400)
					if err != nil {
						panic(err)
					}
					err = os.Rename(fmt.Sprintf("%s/after.cursor.tmp", args.StateDir), fmt.Sprintf("%s/after.cursor", args.StateDir))
					if err != nil {
						panic(err)
					}
				}
				mutex.Lock()
				entriesBatch = entriesBatch[entryCount:]
				mutex.Unlock()
			}
		}
	}()

	runner.StreamJournaldEntries(args.StateDir, func(timestamp time.Time, id string, entryData runner.EntryData) {
		entryJson, err := json.Marshal(entryData)
		if err != nil {
			panic(err)
		}

		entry := loggingingestion.LogEntry{
			Data: common.String(string(entryJson)),
			Id:   common.String(id),
			Time: &common.SDKTime{Time: timestamp},
		}
		mutex.Lock()
		entriesBatch = append(entriesBatch, entry)
		mutex.Unlock()
	})
}
