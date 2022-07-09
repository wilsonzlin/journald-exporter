package runner

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type SystemdCursor struct {
	seqnumId  []byte
	seqnum    []byte
	bootId    []byte
	monotonic []byte
	realtime  []byte
	xorHash   []byte
}

// Derived from sd_journal_get_cursor.
func parseSystemdCursor(raw string) (res SystemdCursor) {
	for _, e := range strings.Split(raw, ";") {
		posOfEq := strings.IndexRune(e, '=')
		v, err := hex.DecodeString(e[posOfEq+1:])
		if err != nil {
			panic(err)
		}
		switch e[:posOfEq] {
		case "s":
			res.seqnumId = v
		case "i":
			res.seqnum = v
		case "b":
			res.bootId = v
		case "m":
			res.monotonic = v
		case "t":
			res.realtime = v
		case "x":
			res.xorHash = v
		}
	}
	return
}

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
	var stateExpectedBytes uint64
	stateExpectedBytesIsDefined := false
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
					assertState(!stateExpectedBytesIsDefined)
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
				stateExpectedBytes = binary.LittleEndian.Uint64(chunk)
				stateExpectedBytesIsDefined = true
				state = ParseStateValue
				chunk = chunk[8:]
			} else if state == ParseStateValue {
				var value []byte
				if !stateExpectedBytesIsDefined {
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
					if uint64(len(stateGotBytes)) < stateExpectedBytes+1 {
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
				stateExpectedBytesIsDefined = false
				fieldName = ""
			}
		}
	}
}

type EntryData struct {
	// A unique, lexicographically-sortable URL-safe ID for this entry, derived from (but not the same as) the journald entry cursor.
	Id       string            `json:"id"`
	Field    map[string]string `json:"field"`
	Message  string            `json:"message"`
	Priority uint64            `json:"priority"`
}

func ok(err error) {
	if err != nil {
		panic(err)
	}
}

func ok2(n int, err error) {
	if err != nil {
		panic(err)
	}
}

func StreamJournaldEntries(stateDir string, onEntryData func(timestamp time.Time, cursor string, entryData EntryData)) {
	var afterCursor string
	if stateDir != "" {
		raw, err := os.ReadFile(fmt.Sprintf("%s/after.cursor", stateDir))
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

	journaldExportParser(jctlOut, func(entryRaw map[string]string) {
		entryTimestampUsRaw, err := strconv.ParseInt(entryRaw["__REALTIME_TIMESTAMP"], 10, 64)
		if err != nil {
			panic(err)
		}
		delete(entryRaw, "__REALTIME_TIMESTAMP")
		timestamp := time.UnixMicro(entryTimestampUsRaw)

		cursorRaw := entryRaw["__CURSOR"]
		delete(entryRaw, "__CURSOR")
		cursor := parseSystemdCursor(cursorRaw)
		// Only seqnumId and seqnum should be needed for uniquely identifying an entry: https://systemd.io/JOURNAL_FILE_FORMAT/.
		// We don't use the whole cursor as it's not lexicographically ordered, so while we can use as ID we can't use as tiebreaker for when two entries have the same timestamp.
		// We need seqnumId to be a fixed length as we're concatenating it with seqnum without any delimiter.
		if len(cursor.seqnumId) != 16 {
			panic(fmt.Errorf("got journald entry cursor with %d-byte seqnumId", len(cursor.seqnumId)))
		}
		idBuf := new(strings.Builder)
		idEncoder := base64.NewEncoder(base64.RawURLEncoding, idBuf)
		ok2(idEncoder.Write(cursor.seqnumId))
		ok2(idEncoder.Write(cursor.seqnum))
		ok(idEncoder.Close())
		id := idBuf.String()

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
			Id:       id,
			Field:    entryRaw,
			Message:  message,
			Priority: priority,
		}

		onEntryData(timestamp, cursorRaw, entryData)
	})
}
