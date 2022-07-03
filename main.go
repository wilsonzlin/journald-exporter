package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/jessevdk/go-flags"
	"github.com/oracle/oci-go-sdk/common"
	"github.com/oracle/oci-go-sdk/common/auth"
	"github.com/oracle/oci-go-sdk/loggingingestion"
)

type ParseState int

const (
	ParseStateName  ParseState = iota
	ParseStateSize             = iota
	ParseStateValue            = iota
)

func indexOf(haystack []byte, needle byte) int {
	for i, c := range haystack {
		if c == needle {
			return i
		}
	}
	return -1
}

func clearMap(m map[string]string) {
	for k := range m {
		delete(m, k)
	}
}

func assertState(cond bool) {
	if !cond {
		panic("Assertion failed")
	}
}

func journaldExportParser(o io.ReadCloser, onEntry func(entry map[string]string)) {
	entry := make(map[string]string)
	var fieldName string
	state := ParseStateName
	stateExpectedBytes := -1
	var stateGotBytes []byte
	push := func(chunk []byte) {
		stateGotBytes = append(stateGotBytes, chunk...)
	}
	takeAll := func() []byte {
		r := stateGotBytes
		stateGotBytes = nil
		return r
	}

	var readBuf [1024 * 1024 * 16]byte
	for {
		n, err := o.Read(readBuf[:])
		if err != nil {
			panic(err)
		}
		chunk := readBuf[0:n]
		for {
			if state == ParseStateName {
				posOfEq := indexOf(chunk, '=')
				posOfLf := indexOf(chunk, '\n')
				if posOfLf == 0 && len(stateGotBytes) == 0 {
					// Entry ended.
					assertState(len(fieldName) == 0)
					assertState(stateExpectedBytes == -1)
					onEntry(entry)
					clearMap(entry)
					chunk = chunk[1:]
				} else if posOfEq != -1 && (posOfLf == -1 || posOfLf > posOfEq) {
					// Name will end; value is text.
					push(chunk[0:posOfEq])
					chunk = chunk[posOfEq+1:]
					state = ParseStateValue
					fieldName = string(takeAll())
				} else if posOfLf != -1 && (posOfEq == -1 || posOfEq > posOfLf) {
					// Name will end; value is binary.
					push(chunk[0:posOfLf])
					chunk = chunk[posOfLf+1:]
					state = ParseStateSize
					fieldName = string(takeAll())
				} else {
					// Still in name.
					assertState(posOfEq == -1)
					assertState(posOfLf == -1)
					push(chunk)
					break
				}
			} else if state == ParseStateSize {
				push(chunk)
				if len(stateGotBytes) < 8 {
					// Still in size.
					break
				}
				chunk = takeAll()
				var stateExpectedBytes uint64
				err = binary.Read(bytes.NewReader(chunk), binary.LittleEndian, &stateExpectedBytes)
				if err != nil {
					panic(err)
				}
				state = ParseStateValue
				chunk = chunk[8:]
			} else if state == ParseStateValue {
				var value []byte
				if stateExpectedBytes == -1 {
					posOfLf := indexOf(chunk, '\n')
					if posOfLf == -1 {
						// Still in value.
						push(chunk)
						break
					}
					push(chunk[0:posOfLf])
					value = takeAll()
					chunk = chunk[posOfLf+1:]
				} else {
					push(chunk)
					// Binary value also ends with LF.
					if len(stateGotBytes) < stateExpectedBytes+1 {
						// Still in value.
						break
					}
					chunk = takeAll()
					assertState(chunk[stateExpectedBytes] == '\n')
					value = chunk[0:stateExpectedBytes]
					chunk = chunk[stateExpectedBytes+1:]
				}
				entry[fieldName] = string(value)
				state = ParseStateName
				stateExpectedBytes = -1
				fieldName = ""
			}
		}
	}
}

type EntryData struct {
	Field    map[string]string `json:"field"`
	Message  string            `json:"message"`
	Priority uint64            `json:"priority"`
}

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

	var afterCursor string
	if args.StateDir != "" {
		raw, err := os.ReadFile(fmt.Sprintf("%s/after.cursor", args.StateDir))
		if err != nil && !os.IsNotExist(err) {
			panic(err)
		}
		afterCursor = string(raw)
	}

	jctlargs := make([]string, 0)
	if afterCursor != "" {
		jctlargs = append(jctlargs, fmt.Sprintf("--after-cursor=%s", afterCursor))
	}
	jctlargs = append(jctlargs, "--follow")
	jctlargs = append(jctlargs, "--lines=2147483647")
	jctlargs = append(jctlargs, "--no-pager")
	jctlargs = append(jctlargs, "--output=export")
	jctl := exec.Command("journalctl", jctlargs...)
	jctlOut, err := jctl.StdoutPipe()
	if err != nil {
		panic(err)
	}
	err = jctl.Start()
	if err != nil {
		panic(err)
	}

	provider, err := auth.InstancePrincipalConfigurationProvider()
	if err != nil {
		panic(err)
	}
	client, err := loggingingestion.NewLoggingClientWithConfigurationProvider(provider)
	var entriesBatch []loggingingestion.LogEntry
	var mutex sync.Mutex

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

	journaldExportParser(jctlOut, func(entryRaw map[string]string) {
		entryTimestampUsRaw, err := strconv.ParseInt(entryRaw["__REALTIME_TIMESTAMP"], 10, 64)
		if err != nil {
			panic(err)
		}
		delete(entryRaw, "__REALTIME_TIMESTAMP")

		id := entryRaw["__CURSOR"]
		delete(entryRaw, "__CURSOR")

		priorityRaw, exists := entryRaw["PRIORITY"]
		if !exists {
			priorityRaw = "3"
		}
		delete(entryRaw, "PRIORITY")
		priority, err := strconv.ParseUint(priorityRaw, 10, 8)
		if err != nil {
			panic(err)
		}

		message := entryRaw["MESSAGE"]
		delete(entryRaw, "MESSAGE")

		// Ignored fields.
		delete(entryRaw, "__MONOTONIC_TIMESTAMP")
		delete(entryRaw, "_BOOT_ID")
		delete(entryRaw, "_HOSTNAME")
		delete(entryRaw, "_MACHINE_ID")
		delete(entryRaw, "_SOURCE_MONOTONIC_TIMESTAMP")
		delete(entryRaw, "_SOURCE_REALTIME_TIMESTAMP")

		entryData := EntryData{
			Field:    entryRaw,
			Message:  message,
			Priority: priority,
		}

		entryJson, err := json.Marshal(entryData)
		if err != nil {
			panic(err)
		}

		entry := loggingingestion.LogEntry{
			Data: common.String(string(entryJson)),
			Id:   common.String(id),
			Time: &common.SDKTime{Time: time.Unix(entryTimestampUsRaw/1e6, (entryTimestampUsRaw%1e6)*1e3)},
		}
		mutex.Lock()
		entriesBatch = append(entriesBatch, entry)
		mutex.Unlock()
	})
}
