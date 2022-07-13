package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudwatchlogs"
	"github.com/jessevdk/go-flags"
	"github.com/wilsonzlin/journald-exporter/pkg/runner"
)

func main() {
	var args struct {
		LogGroup  string `long:"log-group"`
		LogStream string `long:"log-stream"`
		StateDir  string `long:"state-dir"`
	}
	_, err := flags.ParseArgs(&args, os.Args[1:])
	if err != nil {
		panic(err)
	}

	sess := session.Must(session.NewSessionWithOptions(session.Options{
		SharedConfigState: session.SharedConfigEnable,
	}))
	client := cloudwatchlogs.New(sess)

	var mutex sync.Mutex
	var entriesBatchCursors []string
	var entriesBatchEvents []*cloudwatchlogs.InputLogEvent

	go func() {
		// Don't delay too long as it could cause growing backlog if logs are on fire.
		MIN_DELAY := 2 * time.Second
		MAX_DELAY := 1 * time.Minute
		delay := MIN_DELAY
		// The minimum length is 1.
		sequenceToken := "0"
		for {
			time.Sleep(delay)

			mutex.Lock()
			entryCount := 0
			contentLength := 0
			for _, e := range entriesBatchEvents {
				// AWS adds 26 bytes to each event, and there are also other JSON properties as well as the JSON syntax itself.
				entryByteSize := len(*e.Message) + 50
				// The API has a Content-Length limit of 1 MiB and a batch size limit of 10,000.
				if contentLength+entryByteSize > 1048576 || entryCount == 10000 {
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
			res, err := client.PutLogEvents(&cloudwatchlogs.PutLogEventsInput{
				LogGroupName:  &args.LogGroup,
				LogStreamName: &args.LogStream,
				SequenceToken: &sequenceToken,
				LogEvents:     entriesBatchEvents[:entryCount],
			})

			if err != nil {
				if t, ok := err.(*cloudwatchlogs.InvalidSequenceTokenException); ok {
					sequenceToken = *t.ExpectedSequenceToken
				} else {
					fmt.Fprintf(os.Stderr, "Failed to PutLogs %d entries: %s\n", entryCount, err)
					delay = delay * 2
					if delay > MAX_DELAY {
						delay = MAX_DELAY
					}
				}
			} else {
				sequenceToken = *res.NextSequenceToken
				delay = MIN_DELAY
				if args.StateDir != "" {
					err := os.WriteFile(fmt.Sprintf("%s/after.cursor.tmp", args.StateDir), []byte(entriesBatchCursors[entryCount-1]), 0o400)
					if err != nil {
						panic(err)
					}
					err = os.Rename(fmt.Sprintf("%s/after.cursor.tmp", args.StateDir), fmt.Sprintf("%s/after.cursor", args.StateDir))
					if err != nil {
						panic(err)
					}
				}
				mutex.Lock()
				entriesBatchCursors = entriesBatchCursors[entryCount:]
				entriesBatchEvents = entriesBatchEvents[entryCount:]
				mutex.Unlock()
			}
		}
	}()

	runner.StreamJournaldEntries(args.StateDir, func(timestamp time.Time, cursor string, entryData runner.EntryData) {
		entryJson, err := json.Marshal(entryData)
		if err != nil {
			panic(err)
		}

		entry := cloudwatchlogs.InputLogEvent{
			// AWS has a limit of 262144 bytes per event. Allow 50 characters for the timestamp and JSON syntax.
			Message:   aws.String(string(entryJson[:262144 - 50])),
			Timestamp: aws.Int64(timestamp.UnixMilli()),
		}
		mutex.Lock()
		entriesBatchCursors = append(entriesBatchCursors, cursor)
		entriesBatchEvents = append(entriesBatchEvents, &entry)
		mutex.Unlock()
	})
}
