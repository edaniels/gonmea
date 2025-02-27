package common

// Originally from https://github.com/canboat/canboat (Apache License, Version 2.0)
// (C) 2009-2023, Kees Verruijt, Harlingen, The Netherlands.

// This file is part of CANboat.

// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at

//     http://www.apache.org/licenses/LICENSE-2.0

// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// RawMessage is a raw NMEA 2000 PGN message.
type RawMessage struct {
	Timestamp string
	Prio      uint8
	PGN       uint32
	Dst       uint8
	Src       uint8
	Len       uint8
	Data      [FastPacketMaxSize]byte
}

func findOccurrence(msg []byte, c rune, count int) int {
	if len(msg) == 0 || msg[0] == '\n' {
		return 0
	}

	cBytes := []byte{byte(c)}
	pIdx := 0
	for i := 0; i < count && len(msg) != pIdx-1; i++ {
		nextIdx := bytes.Index(msg[pIdx:], cBytes)
		if nextIdx == -1 {
			return -1
		}
		pIdx += nextIdx
		if len(msg) != pIdx-1 {
			pIdx++
		}
	}
	return pIdx
}

// ParseRawFormatPlain parses PLAIN messages.
func ParseRawFormatPlain(msg []byte, m *RawMessage, showJSON bool, logger *Logger) int {
	var prio, pgn, dst, src, dataLen, junk, r int
	var data [8]int

	pIdx := findOccurrence(msg, ',', 1)
	if pIdx == -1 {
		return 1
	}
	pIdx-- // Back to comma

	m.Timestamp = string(msg[:pIdx])

	r, _ = fmt.Sscanf(string(msg[pIdx:]),
		",%d,%d,%d,%d,%d"+
			",%x,%x,%x,%x,%x,%x,%x,%x,%x",
		&prio,
		&pgn,
		&src,
		&dst,
		&dataLen,
		&data[0],
		&data[1],
		&data[2],
		&data[3],
		&data[4],
		&data[5],
		&data[6],
		&data[7],
		&junk)
	if r < 5 {
		//nolint:errcheck
		logger.Error("Error reading message, scanned %d from %s", r, string(msg))
		if !showJSON {
			fmt.Fprintf(logger.writer, "%s", string(msg))
		}
		return 2
	}

	if dataLen > 8 {
		// This is not PLAIN format but FAST format */
		return -1
	}

	if r <= 5+8 {
		for i := 0; i < dataLen; i++ {
			m.Data[i] = uint8(data[i])
		}
	} else {
		return -1
	}

	return setParsedValues(m, prio, pgn, dst, src, dataLen)
}

func setParsedValues(m *RawMessage, prio, pgn, dst, src, dataLen int) int {
	m.Prio = uint8(prio)
	m.PGN = uint32(pgn)
	m.Dst = uint8(dst)
	m.Src = uint8(src)
	m.Len = uint8(dataLen)

	return 0
}

// ParseRawFormatFast parses FAST messages.
func ParseRawFormatFast(msg []byte, m *RawMessage, showJSON bool, logger *Logger) int {
	var prio, pgn, dst, src, dataLen, r int

	pIdx := findOccurrence(msg, ',', 1)
	if pIdx == -1 {
		return 1
	}
	pIdx-- // Back to comma

	m.Timestamp = string(msg[:pIdx])

	r, _ = fmt.Sscanf(string(msg[pIdx:]), ",%d,%d,%d,%d,%d ", &prio, &pgn, &src, &dst, &dataLen)
	if r < 5 {
		//nolint:errcheck
		logger.Error("Error reading message, scanned %d from %s", r, string(msg))
		if !showJSON {
			fmt.Fprintf(logger.writer, "%s", string(msg))
		}
		return 2
	}

	nextIdx := findOccurrence(msg[pIdx:], ',', 6)
	if nextIdx == -1 {
		//nolint:errcheck
		logger.Error("Error reading message, scanned %d bytes from %s", pIdx, string(msg))
		if !showJSON {
			fmt.Fprintf(logger.writer, "%s", string(msg))
		}
		return 2
	}
	pIdx += nextIdx
	for i := 0; i < dataLen; i++ {
		advancedBy, ok := scanHex(msg[pIdx:], &m.Data[i])
		if !ok {
			//nolint:errcheck
			logger.Error("Error reading message, scanned %d bytes from %s/%s, index %d", pIdx, string(msg), string(msg[pIdx:]), i)
			if !showJSON {
				fmt.Fprintf(logger.writer, "%s", string(msg))
			}
			return 2
		}
		pIdx += advancedBy
		if i < dataLen && pIdx < len(msg) {
			if msg[pIdx] != ',' && !unicode.IsSpace(rune(msg[pIdx])) {
				//nolint:errcheck
				logger.Error("Error reading message, scanned %d bytes from %s", pIdx, string(msg))
				if !showJSON {
					fmt.Fprintf(logger.writer, "%s", string(msg))
				}
				return 2
			}
			pIdx++
		}
	}

	return setParsedValues(m, prio, pgn, dst, src, dataLen)
}

func scanNibble(c byte) byte {
	if unicode.IsDigit(rune(c)) {
		return c - '0'
	}
	if c >= 'A' && c <= 'F' {
		return c - 'A' + 10
	}
	if c >= 'a' && c <= 'f' {
		return c - 'a' + 10
	}
	return 16
}

func scanHex(p []byte, m *byte) (int, bool) {
	var hi, lo byte

	if p[0] == 0 || p[1] == 0 {
		return 0, false
	}

	hi = scanNibble(p[0])
	if hi > 15 {
		return 0, false
	}
	lo = scanNibble(p[1])
	if lo > 15 {
		return 0, false
	}
	*m = hi<<4 | lo
	/* printf("(b=%02X,p=%p) ", *m, *p); */
	return 2, true
}

var tiden int

// ParseRawFormatActisenseN2KAscii parses Actisense N2K ASCII messages.
func ParseRawFormatActisenseN2KAscii(msg []byte, m *RawMessage, showJSON bool, logger *Logger) int {
	scanned := 0

	// parse timestamp. Actisense doesn't give us date so let's figure it out ourself
	splitBySpaces := strings.Split(string(msg), " ")
	if len(splitBySpaces) == 1 || splitBySpaces[0][0] != 'A' {
		//nolint:errcheck
		logger.Error("No message or does not start with 'A'\n")
		return -1
	}

	var secs, millis int
	r, _ := fmt.Sscanf(splitBySpaces[0][1:], "%d.%d", &secs, &millis)
	if r < 1 {
		return -1
	}

	if tiden == 0 {
		tiden = int(logger.Now().Unix()) - secs
	}
	now := tiden + secs

	//nolint:gosmopolitan
	tm := time.Unix(int64(now), 0).Local()
	m.Timestamp = tm.Format("2006-01-02T15:04:05")
	m.Timestamp = fmt.Sprintf("%s,%3.3d", m.Timestamp, millis)

	// parse <SRC><DST><P>
	scanned += len(splitBySpaces[0]) + 1
	splitBySpaces = splitBySpaces[1:]
	if len(splitBySpaces) == 0 {
		return -1
	}
	//nolint:errcheck
	n, _ := strconv.ParseInt(splitBySpaces[0], 16, 64)
	m.Prio = uint8(n & 0xf)
	m.Dst = uint8((n >> 4) & 0xff)
	m.Src = uint8((n >> 12) & 0xff)

	// parse <PGN>
	scanned += len(splitBySpaces[0]) + 1
	splitBySpaces = splitBySpaces[1:]
	if len(splitBySpaces) == 0 {
		//nolint:errcheck
		logger.Error("Incomplete message\n")
		if !showJSON {
			fmt.Fprintf(logger.writer, "%s", msg)
		}
		return -1
	}
	//nolint:errcheck
	n, _ = strconv.ParseInt(splitBySpaces[0], 16, 64)
	m.PGN = uint32(n)

	// parse DATA
	scanned += len(splitBySpaces[0]) + 1
	p := []byte(strings.Join(splitBySpaces[1:], " "))
	var i uint8
	for i = 0; i < FastPacketMaxSize; i++ {
		if len(p) == 0 || unicode.IsSpace(rune(p[0])) {
			break
		}
		advancedBy, ok := scanHex(p, &m.Data[i])
		if !ok {
			//nolint:errcheck
			logger.Error("Error reading message, scanned %d bytes from %s/%s, index %d", len(msg)-scanned, string(msg), string(p), i)
			if !showJSON {
				fmt.Fprintf(logger.writer, "%s", msg)
			}
			return 2
		}
		scanned += advancedBy
		p = p[advancedBy:]
	}
	m.Len = i

	return 0
}

// ParseRawFormatAirmar parses Airmar messages.
// Note(UNTESTED): See README.md.
func ParseRawFormatAirmar(msg []byte, m *RawMessage, showJSON bool, logger *Logger) int {
	var prio, pgn, dst, src, dataLen uint
	var id uint

	pIdx := findOccurrence(msg, ' ', 1)
	if pIdx < 4 || pIdx >= 60 {
		return 1
	}

	m.Timestamp = string(msg[:pIdx-1])
	pIdx += 3

	r, _ := fmt.Sscanf(string(msg[pIdx:]), "%d", &pgn)
	if r != 1 {
		//nolint:errcheck
		logger.Error("Error reading message, scanned %d bytes from %s", pIdx, string(msg))
		if !showJSON {
			fmt.Fprintf(logger.writer, "%s", msg)
		}
		return 2
	}
	pIdx += len(strconv.FormatUint(uint64(pgn), 10))
	if msg[pIdx] == ' ' {
		pIdx++

		r, _ := fmt.Sscanf(string(msg[pIdx:]), "%x", &id)
		if r != 1 {
			//nolint:errcheck
			logger.Error("Error reading message, scanned %d bytes from %s", pIdx, string(msg))
			if !showJSON {
				fmt.Fprintf(logger.writer, "%s", msg)
			}
			return 2
		}
		pIdx += len(strconv.FormatUint(uint64(id), 16))
	}
	if msg[pIdx] != ' ' {
		//nolint:errcheck
		logger.Error("Error reading message, scanned %d bytes from %s", pIdx, string(msg))
		if !showJSON {
			fmt.Fprintf(logger.writer, "%s", msg)
		}
		return 2
	}

	getISO11783BitsFromCanID(id, &prio, &pgn, &src, &dst)

	pIdx++
	dataLen = uint(len(msg[pIdx:]) / 2)
	for i := uint(0); i < dataLen; i++ {
		advancedBy, ok := scanHex(msg[pIdx:], &m.Data[i])
		if !ok {
			//nolint:errcheck
			logger.Error("Error reading message, scanned %d bytes from %s/%s, index %d", pIdx, string(msg), string(msg[pIdx:]), i)
			if !showJSON {
				fmt.Fprintf(logger.writer, "%s", msg)
			}
			return 2
		}
		pIdx += advancedBy
		if i < dataLen {
			if msg[pIdx] != ',' && msg[pIdx] != ' ' {
				//nolint:errcheck
				logger.Error("Error reading message, scanned %d bytes from %s", pIdx, string(msg))
				if !showJSON {
					fmt.Fprintf(logger.writer, "%s", msg)
				}
				return 2
			}
			pIdx++
		}
	}

	return setParsedValues(m, int(prio), int(pgn), int(dst), int(src), int(dataLen))
}

// ParseRawFormatChetco parses Chetco messages.
// Note(UNTESTED): See README.md.
func ParseRawFormatChetco(msg []byte, m *RawMessage, showJSON bool, logger *Logger) int {
	var pgn, src uint
	var tstamp uint

	if len(msg) == 0 || msg[0] == '\n' {
		return 1
	}

	if r, _ := fmt.Sscanf(string(msg), "$PCDIN,%x,%x,%x,", &pgn, &tstamp, &src); r < 3 {
		//nolint:errcheck
		logger.Error("Error reading Chetco message: %s", msg)
		if !showJSON {
			fmt.Fprintf(logger.writer, "%s", msg)
		}
		return 2
	}

	t := int(tstamp / 1000)
	//nolint:gosmopolitan
	tm := time.Unix(int64(t), 0).Local()
	m.Timestamp = tm.Format("2006-01-02T15:04:05")
	m.Timestamp = fmt.Sprintf("%s,%3.3d", m.Timestamp, tstamp%1000)

	pIdx := len("$PCDIN,01FD07,089C77D!,03,") // Fixed length where data bytes start;

	var i uint
	for i = 0; msg[pIdx] != '*'; i++ {
		advancedBy, ok := scanHex(msg[pIdx:], &m.Data[i])
		if !ok {
			//nolint:errcheck
			logger.Error("Error reading message, scanned %d bytes from %s/%s, index %d", pIdx, string(msg), string(msg[pIdx:]), i)
			if !showJSON {
				fmt.Fprintf(logger.writer, "%s", msg)
			}
			return 2
		}
		pIdx += advancedBy
	}

	return setParsedValues(m, 0, int(pgn), 255, int(src), int(i+1))
}

/*
ParseRawFormatGarminCSV parses Garmin CSV (1 and 2) messages.

Sequence #,Timestamp,PGN,Name,Manufacturer,Remote Address,Local Address,Priority,Single Frame,Size,Packet
0,486942,127508,Battery Status,Garmin,6,255,2,1,8,0x017505FF7FFFFFFF
129,491183,129029,GNSS Position Data,Unknown
Manufacturer,3,255,3,0,43,0xFFDF40A6E9BB22C04B3666C18FBF0600A6C33CA5F84B01A0293B140000000010FC01AC26AC264A12000000
*/
// Note(UNTESTED): See README.md.
func ParseRawFormatGarminCSV(msg []byte, m *RawMessage, showJSON, absolute bool, logger *Logger) int {
	var seq, tstamp, pgn, src, dst, prio, single, count uint
	var t int
	var tm time.Time

	if len(msg) == 0 || msg[0] == '\n' {
		return 1
	}

	var pIdx int
	if absolute {
		var month, day, year, hours, minutes, seconds, ms uint

		if r, _ := fmt.Sscanf(
			string(msg),
			"%d,%d_%d_%d_%d_%d_%d_%d,%d,",
			&seq, &month, &day, &year, &hours, &minutes, &seconds, &ms, &pgn); r < 9 {
			//nolint:errcheck
			logger.Error("Error reading Garmin CSV message: %s", msg)
			if !showJSON {
				fmt.Fprintf(logger.writer, "%s", msg)
			}
			return 2
		}
		m.Timestamp = fmt.Sprintf(
			"%04d-%02d-%02dT%02d:%02d:%02d,%03d",
			year,
			month,
			day,
			hours,
			minutes,
			seconds,
			ms%1000)

		pIdx = findOccurrence(msg, ',', 6)
	} else {
		if r, _ := fmt.Sscanf(string(msg), "%d,%d,%d,", &seq, &tstamp, &pgn); r < 3 {
			//nolint:errcheck
			logger.Error("Error reading Garmin CSV message: %s", msg)
			if !showJSON {
				fmt.Fprintf(logger.writer, "%s", msg)
			}
			return 2
		}

		t = int(tstamp / 1000)
		//nolint:gosmopolitan
		tm = time.Unix(int64(t), 0).Local()
		m.Timestamp = tm.Format("2006-01-02T15:04:05")
		m.Timestamp = fmt.Sprintf("%s,%3.3d", m.Timestamp, tstamp%1000)

		pIdx = findOccurrence(msg, ',', 5)
	}

	if len(msg[pIdx:]) == 0 {
		//nolint:errcheck
		logger.Error("Error reading Garmin CSV message: %s", msg)
		if !showJSON {
			fmt.Fprintf(logger.writer, "%s", msg)
		}
		return 3
	}

	var restOfData string
	if r, _ := fmt.Sscanf(string(msg[pIdx:]), "%d,%d,%d,%d,%d,0x%s", &src, &dst, &prio, &single, &count, &restOfData); r < 5 {
		//nolint:errcheck
		logger.Error("Error reading Garmin CSV message: %s", msg)
		if !showJSON {
			fmt.Fprintf(logger.writer, "%s", msg)
		}
		return 3
	}
	pIdx += strings.Index(string(msg[pIdx:]), ",0x") + 3

	var i uint
	for i = 0; len(msg[pIdx:]) != 0 && i < count; i++ {
		advancedBy, ok := scanHex(msg[pIdx:], &m.Data[i])
		if !ok {
			//nolint:errcheck
			logger.Error("Error reading message, scanned %d bytes from %s/%s, index %d", pIdx, string(msg), string(msg[pIdx:]), i)
			if !showJSON {
				fmt.Fprintf(logger.writer, "%s", msg)
			}
			return 2
		}
		pIdx += advancedBy
	}

	return setParsedValues(m, int(prio), int(pgn), int(dst), int(src), int(i+1))
}

//nolint:dupword
/*
ParseRawFormatYDWG02 parses YDWG-02 messages.

Yacht Digital, YDWG-02

   Example output: 00:17:55.475 R 0DF50B23 FF FF FF FF FF 00 00 FF

   Example usage:

pi@yacht:~/canboat/analyzer $ netcat 192.168.3.2 1457 | analyzer -json
INFO 2018-10-16T09:57:39.665Z [analyzer] Detected YDWG-02 protocol with all data on one line
INFO 2018-10-16T09:57:39.665Z [analyzer] New PGN 128267 for device 35 (heap 5055 bytes)
{"timestamp":"2018-10-16T22:25:25.166","prio":3,"src":35,"dst":255,"pgn":128267,"description":"Water
Depth","fields":{"Offset":0.000}} INFO 2018-10-16T09:57:39.665Z [analyzer] New PGN 128259 for device 35 (heap 5070 bytes)
{"timestamp":"2018-10-16T22:25:25.177","prio":2,"src":35,"dst":255,"pgn":128259,"description":"Speed","fields":{"Speed Water
Referenced":0.00,"Speed Water Referenced Type":"Paddle wheel"}} INFO 2018-10-16T09:57:39.666Z [analyzer] New PGN 128275 for device
35 (heap 5091 bytes)
{"timestamp":"2018-10-16T22:25:25.179","prio":6,"src":35,"dst":255,"pgn":128275,"description":"Distance
Log","fields":{"Date":"1980.05.04"}} INFO 2018-10-16T09:57:39.666Z [analyzer] New PGN 130311 for device 35 (heap 5106 bytes)
{"timestamp":"2018-10-16T22:25:25.181","prio":5,"src":35,"dst":255,"pgn":130311,"description":"Environmental
Parameters","fields":{"Temperature Source":"Sea Temperature","Temperature":13.39}}
{"timestamp":"2018-10-16T22:25:25.181","prio":6,"src":35,"dst":255,"pgn":128275,"description":"Distance
Log","fields":{"Date":"2006.11.06", "Time": "114:38:39.07076","Log":1940}}
{"timestamp":"2018-10-16T22:25:25.185","prio":6,"src":35,"dst":255,"pgn":128275,"description":"Distance
Log","fields":{"Date":"1970.07.14"}} INFO 2018-10-16T09:57:39.666Z [analyzer] New PGN 130316 for device 35 (heap 5121 bytes)
{"timestamp":"2018-10-16T22:25:25.482","prio":5,"src":35,"dst":255,"pgn":130316,"description":"Temperature Extended
Range","fields":{"Instance":0,"Source":"Sea Temperature","Temperature":13.40}}
{"timestamp":"2018-10-16T22:25:25.683","prio":5,"src":35,"dst":255,"pgn":130311,"description":"Environmental
Parameters","fields":{"Temperature Source":"Sea Temperature","Temperature":13.39}}
*/
// Note(UNTESTED): See README.md.
func ParseRawFormatYDWG02(msg []byte, m *RawMessage, logger *Logger) int {
	var msgid uint
	var prio, pgn, src, dst uint

	// parse timestamp. YDWG doesn't give us date so let's figure it out ourself
	splitBySpaces := strings.Split(string(msg), " ")
	if len(splitBySpaces) == 1 {
		return -1
	}
	tiden := logger.Now().Unix()
	//nolint:gosmopolitan
	tm := time.Unix(tiden, 0).Local()
	m.Timestamp = tm.Format("2006-01-02T")
	m.Timestamp = fmt.Sprintf("%s%s", m.Timestamp, splitBySpaces[0])

	// parse direction, not really used in analyzer
	splitBySpaces = splitBySpaces[1:]
	if len(splitBySpaces) == 0 {
		return -1
	}

	// parse msgid
	splitBySpaces = splitBySpaces[1:]
	if len(splitBySpaces) == 0 {
		return -1
	}
	//nolint:errcheck
	n, _ := strconv.ParseInt(splitBySpaces[0], 16, 64)
	msgid = uint(n)
	getISO11783BitsFromCanID(msgid, &prio, &pgn, &src, &dst)

	// parse data
	i := 0
	for splitBySpaces = splitBySpaces[1:]; len(splitBySpaces) != 0; splitBySpaces = splitBySpaces[1:] {
		//nolint:errcheck
		n, _ := strconv.ParseInt(splitBySpaces[0], 16, 64)
		m.Data[i] = byte(n)
		i++
		if i > FastPacketMaxSize {
			return -1
		}
	}

	return setParsedValues(m, int(prio), int(pgn), int(dst), int(src), i)
}

// ParseRawFormatNavLink2 parses Digital Yacht NavLink 2 messages.
// https://github.com/digitalyacht/iKonvert/wiki/4.-Serial-Protocol#41-rx-pgn-sentence
// !PDGY,<pgn#>,p,src,dst,timer,<pgn_data> CR LF
//
// # Key
//
// <pgn#> = NMEA2000 PGN number between 0 and 999999
//
// p = Priority 0-7 with 0 being highest and 7 lowest
//
// src = Source Address of the device sending the PGN between 0-251
//
// dst = Destination Address of the device receiving the PGN between 0-255 (255 = global)
//
// timer = internal timer of the gateway in milliseconds 0-999999
//
// <pgn_data> = The binary payload of the PGN encoded in Base64.
func ParseRawFormatNavLink2(msg []byte, m *RawMessage, logger *Logger) int {
	var pgn, prio, src, dst int
	var timer float64
	var pgnData string
	r, _ := fmt.Sscanf(string(msg), "!PDGY,%d,%d,%d,%d,%f,%s ", &pgn, &prio, &src, &dst, &timer, &pgnData)
	if r != 6 {
		//nolint:errcheck
		logger.Error("wrong amount of fields in message: %d", r)
		return -1
	}

	m.Timestamp = strconv.FormatFloat(timer, 'f', 2, 64)

	decoded, err := base64.RawStdEncoding.DecodeString(pgnData)
	if err != nil {
		//nolint:errcheck
		logger.Error("error decoding base64 data: %s", err)
		return -1
	}
	copy(m.Data[:], decoded)

	return setParsedValues(m, prio, pgn, dst, src, len(decoded))
}
