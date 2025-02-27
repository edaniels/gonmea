// Package analyzer analyzes NMEA 2000 PGN messages
package analyzer

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
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"unicode"

	"github.com/erh/gonmea/common"
)

func init() {
	initLookupTypes()
	initFieldTypes()
	initPGNs()
}

// An Analyzer analyzes NMEA 2000 PGN messages.
type Analyzer struct {
	Config
	sep                 string
	closingBraces       string // } and ] chars to close sentence in JSON mode, otherwise empty string
	variableFieldRepeat [2]int64
	currentDate         uint16
	currentTime         uint32
	refPgn              int64 // Remember this over the entire set of fields
	length              int64
	skip                bool
	previousFieldValue  int64
	ftf                 *pgnField

	pb               printBuffer
	fieldTypes       []fieldType
	pgns             []pgnInfo
	reassemblyBuffer [reassemblyBufferSize]packet
	reader           *bufio.Reader
}

// NewAnalyzer returns a new analyzer using the given config.
func NewAnalyzer(conf *Config) (*Analyzer, error) {
	ana := &Analyzer{
		Config:              *conf,
		sep:                 " ",
		variableFieldRepeat: [2]int64{0, 0}, // Actual number of repetitions
		currentDate:         math.MaxUint16,
		currentTime:         math.MaxUint32,

		fieldTypes: make([]fieldType, len(immutFieldTypes)),
		pgns:       make([]pgnInfo, len(immutPGNs)),
		reader:     bufio.NewReader(conf.InFile),
	}

	copy(ana.fieldTypes, immutFieldTypes)
	copy(ana.pgns, immutPGNs)

	if conf.CamelCase != nil {
		ana.camelCase(*conf.CamelCase)
	}

	ana.fillLookups()
	if err := ana.fillFieldType(true); err != nil {
		return nil, err
	}
	if err := ana.checkPGNs(); err != nil {
		return nil, err
	}

	return ana, nil
}

// Config is used to configure an Analyzer.
type Config struct {
	ShowRaw        bool
	ShowData       bool
	ShowBytes      bool
	ShowJSON       bool
	ShowJSONEmpty  bool
	ShowJSONValue  bool
	ShowVersion    bool
	showSI         bool
	ShowGeo        geoFormat
	OnlyPgn        int64
	OnlySrc        int64
	OnlyDst        int64
	ClockSrc       int64
	SelectedFormat rawFormat
	multipackets   multipackets
	CamelCase      *bool
	InFile         io.Reader
	OutFile        io.Writer
	OutErrFile     io.Writer
	Logger         *common.Logger
}

// NewConfigForCLI returns a config for use with a CLI.
func NewConfigForCLI() *Config {
	return newConfig(os.Stdout, os.Stderr, common.NewLoggerForCLI(os.Stderr))
}

// NewConfigForLibrary returns a config for use with a library.
func NewConfigForLibrary(
	logger *common.Logger,
) *Config {
	return newConfig(io.Discard, io.Discard, logger)
}

func newConfig(
	outFile io.Writer,
	outErrFile io.Writer,
	logger *common.Logger,
) *Config {
	return &Config{
		ShowRaw:        false,
		ShowData:       false,
		ShowBytes:      false,
		ShowJSON:       false,
		ShowJSONEmpty:  false,
		ShowJSONValue:  false,
		ShowVersion:    true,
		showSI:         false, // Output everything in strict SI units
		ShowGeo:        geoFormatDD,
		OnlyPgn:        int64(0),
		OnlySrc:        int64(-1),
		OnlyDst:        int64(-1),
		ClockSrc:       int64(-1),
		SelectedFormat: rawFormatUnknown,
		multipackets:   multipacketsSeparate,
		Logger:         logger,
		OutFile:        outFile,
		OutErrFile:     outErrFile,
	}
}

// ParseArgs parses the args of a CLI program into a Config.
func ParseArgs(args []string) (*Config, bool, error) {
	progNameAsExeced := args[0]

	conf := NewConfigForCLI()
	conf.Logger.SetProgName(progNameAsExeced)

	conf.InFile = os.Stdin
	for argIdx := 1; argIdx < len(args); argIdx++ {
		arg := args[argIdx]
		hasNext := argIdx < len(args)-1

		//nolint:gocritic
		if strings.EqualFold(arg, "-version") {
			fmt.Fprintf(conf.OutFile, "%s\n", common.Version)
			return nil, false, nil
		} else if strings.EqualFold(arg, "-schema-version") {
			fmt.Fprintf(conf.OutFile, "%s\n", common.SchemaVersion)
			return nil, false, nil
		} else if strings.EqualFold(arg, "-camel") {
			conf.CamelCase = &falseValue
		} else if strings.EqualFold(arg, "-upper-camel") {
			conf.CamelCase = &trueValue
		} else if strings.EqualFold(arg, "-raw") {
			conf.ShowRaw = true
		} else if strings.EqualFold(arg, "-debug") {
			conf.ShowJSONEmpty = true
			conf.ShowBytes = true
		} else if strings.EqualFold(arg, "-d") {
			conf.Logger.SetLogLevel(common.LogLevelDebug)
		} else if strings.EqualFold(arg, "-q") {
			conf.Logger.SetLogLevel(common.LogLevelError)
		} else if hasNext && strings.EqualFold(arg, "-geo") {
			nextArg := args[argIdx+1]
			if strings.EqualFold(nextArg, "dd") {
				conf.ShowGeo = geoFormatDD
			} else if strings.EqualFold(nextArg, "dm") {
				conf.ShowGeo = geoFormatDM
			} else if strings.EqualFold(nextArg, "dms") {
				conf.ShowGeo = geoFormatDMS
			} else {
				return nil, false, usage(progNameAsExeced, nextArg, conf.OutFile)
			}
			argIdx++
		} else if strings.EqualFold(arg, "-si") {
			conf.showSI = true
		} else if strings.EqualFold(arg, "-nosi") {
			conf.showSI = false
		} else if strings.EqualFold(arg, "-json") {
			conf.ShowJSON = true
		} else if strings.EqualFold(arg, "-empty") {
			conf.ShowJSONEmpty = true
			conf.ShowJSON = true
		} else if strings.EqualFold(arg, "-nv") {
			conf.ShowJSONValue = true
			conf.ShowJSON = true
		} else if strings.EqualFold(arg, "-data") {
			conf.ShowData = true
		} else if hasNext && strings.EqualFold(arg, "-fixtime") {
			nextArg := args[argIdx+1]
			conf.Logger.SetFixedTimestamp(nextArg)
			if !strings.Contains(nextArg, "n2kd") {
				conf.ShowVersion = false
			}
			argIdx++
		} else if hasNext && strings.EqualFold(arg, "-src") {
			nextArg := args[argIdx+1]
			//nolint:errcheck
			conf.OnlySrc, _ = strconv.ParseInt(nextArg, 10, 64)
			argIdx++
		} else if hasNext && strings.EqualFold(arg, "-dst") {
			nextArg := args[argIdx+1]
			//nolint:errcheck
			conf.OnlyDst, _ = strconv.ParseInt(nextArg, 10, 64)
			argIdx++
		} else if hasNext && strings.EqualFold(arg, "-Clocksrc") {
			nextArg := args[argIdx+1]
			//nolint:errcheck
			conf.ClockSrc, _ = strconv.ParseInt(nextArg, 10, 64)
			argIdx++
		} else if hasNext && strings.EqualFold(arg, "-file") {
			nextArg := args[argIdx+1]
			var err error
			//nolint:gosec
			conf.InFile, err = os.OpenFile(nextArg, os.O_RDONLY, 0)
			if err != nil {
				return nil, false, conf.Logger.Abort("Cannot open file %s\n", nextArg)
			}
			argIdx++
		} else if hasNext && strings.EqualFold(arg, "-format") {
			nextArg := args[argIdx+1]
			for _, format := range rawFormats {
				if strings.EqualFold(nextArg, string(format)) {
					conf.SelectedFormat = format
					if conf.SelectedFormat != rawFormatPlain && conf.SelectedFormat != rawFormatPlainOrFast {
						conf.multipackets = multipacketsCoalesced
					}
					break
				}
				if conf.SelectedFormat == rawFormatUnknown {
					return nil, false, conf.Logger.Abort("Unknown message format '%s'\n", nextArg)
				}
			}
			argIdx++
		} else {
			//nolint:errcheck
			conf.OnlyPgn, _ = strconv.ParseInt(arg, 10, 64)
			if conf.OnlyPgn > 0 {
				conf.Logger.Info("Only logging PGN %d\n", conf.OnlyPgn)
			} else {
				return nil, false, usage(progNameAsExeced, arg, conf.OutFile)
			}
		}
	}
	return conf, true, nil
}

// ReadMessage returns the next message read or io.EOF.
func (ana *Analyzer) ReadMessage() ([]byte, error) {
	var buf bytes.Buffer
	if err := ana.readMessage(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (ana *Analyzer) readMessage(writer io.Writer) error {
	for {
		msg, isPrefix, err := ana.reader.ReadLine()
		if err != nil || isPrefix {
			return io.EOF
		}

		if len(msg) == 0 || msg[0] == '\r' || msg[0] == '\n' || msg[0] == '#' {
			if len(msg) != 0 && msg[0] == '#' {
				if bytes.Equal(msg[1:], []byte("SHOWBUFFERS")) {
					ana.showBuffers()
				}
			}

			continue
		}

		if ana.SelectedFormat == rawFormatUnknown {
			ana.SelectedFormat, ana.multipackets = detectFormat(string(msg), ana.Logger)
			if ana.SelectedFormat == rawFormatGarminCSV1 || ana.SelectedFormat == rawFormatGarminCSV2 {
				// Skip first line containing header line
				continue
			}
		}

		m, err := ParseLineWithFormat(msg, ana.SelectedFormat, ana.ShowJSON, ana.Logger)
		if err != nil {
			ana.Logger.Error("Unknown error %v\n", err)
		} else {
			if err := ana.printCanFormat(m, writer); err != nil {
				return err
			}
			ana.printCanRaw(m)
			return nil
		}
	}
}

// Run performs analysis.
func (ana *Analyzer) Run() error {
	if !ana.ShowJSON {
		ana.Logger.Info("N2K packet analyzer\n" + common.Copyright)
	} else if ana.ShowVersion {
		siStr := "si"
		if !ana.showSI {
			siStr = "std"
		}

		jsonValueStr := "true"
		if !ana.ShowJSONValue {
			jsonValueStr = "false"
		}
		fmt.Fprintf(ana.OutFile, "{\"version\":\"%s\",\"units\":\"%s\",\"showLookupValues\":%s}\n",
			common.Version,
			siStr,
			jsonValueStr)
	}

	for {
		if err := ana.readMessage(ana.OutFile); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

type rawFormat string

const (
	rawFormatUnknown           rawFormat = "UNKNOWN"
	rawFormatPlain             rawFormat = "PLAIN"
	rawFormatFast              rawFormat = "FAST"
	rawFormatPlainOrFast       rawFormat = "PLAIN_OR_FAST"
	rawFormatAirmar            rawFormat = "AIRMAR"
	rawFormatChetco            rawFormat = "CHETCO"
	rawFormatGarminCSV1        rawFormat = "GARMIN_CSV1"
	rawFormatGarminCSV2        rawFormat = "GARMIN_CSV2"
	rawFormatYDWG02            rawFormat = "YDWG02"
	rawFormatNavLink2          rawFormat = "NAVLINK2"
	rawFormatActisenseN2KASCII rawFormat = "ACTISENSE_N2K_ASCII"
)

var rawFormats = []rawFormat{
	rawFormatUnknown,
	rawFormatPlain,
	rawFormatFast,
	rawFormatPlainOrFast,
	rawFormatAirmar,
	rawFormatChetco,
	rawFormatGarminCSV1,
	rawFormatGarminCSV2,
	rawFormatYDWG02,
	rawFormatNavLink2,
	rawFormatActisenseN2KASCII,
}

type geoFormat byte

const (
	geoFormatDD geoFormat = iota
	geoFormatDM
	geoFormatDMS
)

type multipackets byte

const (
	multipacketsCoalesced multipackets = iota
	multipacketsSeparate
)

//nolint:lll
func usage(progNameAsExeced, invalidArgName string, writer io.Writer) error {
	fmt.Fprintf(writer, "Unknown or invalid argument %s\n", invalidArgName)
	fmt.Fprintf(writer, "Usage: %s [[-raw] [-json [-empty] [-nv] [-camel | -upper-camel]] [-data] [-debug] [-d] [-q] [-si] [-geo {dd|dm|dms}] "+
		"-format <fmt> "+
		"[-src <src> | -dst <dst> | <pgn>]] ["+
		"-Clocksrc <src> | "+
		"-version\n",
		progNameAsExeced)
	fmt.Fprintf(writer, "     -json             Output in json format, for program consumption. Empty values are skipped\n")
	fmt.Fprintf(writer, "     -empty            Modified json format where empty values are shown as NULL\n")
	fmt.Fprintf(writer, "     -nv               Modified json format where lookup values are shown as name, value pair\n")
	fmt.Fprintf(writer, "     -camel            Show fieldnames in normalCamelCase\n")
	fmt.Fprintf(writer, "     -upper-camel      Show fieldnames in UpperCamelCase\n")
	fmt.Fprintf(writer, "     -d                Print logging from level ERROR, INFO and DEBUG\n")
	fmt.Fprintf(writer, "     -q                Print logging from level ERROR\n")
	fmt.Fprintf(writer, "     -si               Show values in strict SI units: degrees Kelvin, rotation in radians/sec, etc.\n")
	fmt.Fprintf(writer, "     -geo dd           Print geographic format in dd.dddddd format\n")
	fmt.Fprintf(writer, "     -geo dm           Print geographic format in dd.mm.mmm format\n")
	fmt.Fprintf(writer, "     -geo dms          Print geographic format in dd.mm.sss format\n")
	fmt.Fprintf(writer, "     -Clocksrc         Set the systemclock from time info from this NMEA source address\n")
	fmt.Fprintf(writer, "     -format <fmt>     Select a particular format, either: ")
	for _, format := range rawFormats {
		fmt.Fprintf(writer, "%s, ", format)
	}
	fmt.Fprintf(writer, "\n")
	fmt.Fprintf(writer, "     -version          Print the version of the program and quit\n")
	fmt.Fprintf(writer, "\nThe following options are used to debug the analyzer:\n")
	fmt.Fprintf(writer, "     -raw              Print the PGN in a format suitable to be fed to analyzer again (in standard raw format)\n")
	fmt.Fprintf(writer, "     -data             Print the PGN three times: in hex, ascii and analyzed\n")
	fmt.Fprintf(writer, "     -debug            Print raw value per field\n")
	fmt.Fprintf(writer, "     -fixtime str      Print str as timestamp in logging\n")
	fmt.Fprintf(writer, "\n")
	return &common.ExitError{Code: 1}
}

type packet struct {
	size      int
	data      [common.FastPacketMaxSize]uint8
	frames    uint32 // Bit is one when frame is received
	allFrames uint32 // Bit is one when frame needs to be present
	pgn       int
	src       int
	used      bool
}

const reassemblyBufferSize = 64

func (ana *Analyzer) showBuffers() {
	var p *packet

	for buffer := 0; buffer < reassemblyBufferSize; buffer++ {
		p = &ana.reassemblyBuffer[buffer]

		if p.used {
			//nolint:errcheck
			ana.Logger.Error("ReassemblyBuffer[%d] PGN %d: size %d frames=%x mask=%x\n", buffer, p.pgn, p.size, p.frames, p.allFrames)
		} else {
			ana.Logger.Debug("ReassemblyBuffer[%d]: inUse=false\n", buffer)
		}
	}
}

type hexScanner struct {
	val   int
	isSet bool
}

func (h *hexScanner) Scan(state fmt.ScanState, _ rune) error {
	n, err := fmt.Fscanf(state, "%x", &h.val)
	if n > 0 {
		h.isSet = true
	}
	return err
}

func (ana *Analyzer) printCanFormat(
	msg *common.RawMessage,
	writer io.Writer,
) error {
	if ana.OnlySrc >= 0 && ana.OnlySrc != int64(msg.Src) {
		return nil
	}
	if ana.OnlyDst >= 0 && ana.OnlyDst != int64(msg.Dst) {
		return nil
	}
	if ana.OnlyPgn > 0 && ana.OnlyPgn != int64(msg.PGN) {
		return nil
	}

	pgn, _ := ana.searchForPgn(msg.PGN)
	if ana.multipackets == multipacketsSeparate && pgn == nil {
		var err error
		pgn, err = ana.searchForUnknownPgn(msg.PGN)
		if err != nil {
			return err
		}
	}

	if ana.multipackets == multipacketsCoalesced || pgn == nil || pgn.packetType != packetTypeFast {
		// No reassembly needed
		if err := ana.printPgn(msg, msg.Data[:msg.Len], writer); err != nil {
			return err
		}
		return nil
	}

	// Fast packet requires re-asssembly
	// We only get here if we know for sure that the PGN is fast-packet
	// Possibly it is of unknown length when the PGN is unknown.

	var buffer int
	var p *packet
	for buffer = 0; buffer < reassemblyBufferSize; buffer++ {
		p = &ana.reassemblyBuffer[buffer]

		if p.used && p.pgn == int(msg.PGN) && p.src == int(msg.Src) {
			// Found existing slot
			break
		}
	}
	if buffer == reassemblyBufferSize {
		// Find a free slot
		for buffer = 0; buffer < reassemblyBufferSize; buffer++ {
			p = &ana.reassemblyBuffer[buffer]
			if !p.used {
				break
			}
		}
		if buffer == reassemblyBufferSize {
			//nolint:errcheck
			ana.Logger.Error("Out of reassembly buffers; ignoring PGN %d\n", msg.PGN)
			return nil
		}
		p.used = true
		p.src = int(msg.Src)
		p.pgn = int(msg.PGN)
		p.frames = 0
	}

	{
		// YDWG can receive frames out of order, so handle this.
		frame := uint32(msg.Data[0] & 0x1f)
		seq := uint32(msg.Data[0] & 0xe0)

		idx := uint32(0)
		frameLen := common.FastPacketBucket0Size
		msgIdx := common.FastPacketBucket0Offset

		if frame != 0 {
			idx = common.FastPacketBucket0Size + (frame-1)*common.FastPacketBucketNSize
			frameLen = common.FastPacketBucketNSize
			msgIdx = common.FastPacketBucketNOffset
		}

		if (p.frames & (1 << frame)) != 0 {
			//nolint:errcheck
			ana.Logger.Error("Received incomplete fast packet PGN %d from source %d\n", msg.PGN, msg.Src)
			p.frames = 0
		}

		if frame == 0 && p.frames == 0 {
			p.size = int(msg.Data[1])
			p.allFrames = (1 << (1 + (p.size / 7))) - 1
		}

		copy(p.data[idx:], msg.Data[msgIdx:msgIdx+frameLen])
		p.frames |= 1 << frame

		ana.Logger.Debug("Using buffer %d for reassembly of PGN %d: size %d frame %d sequence %d idx=%d frames=%x mask=%x\n",
			buffer,
			msg.PGN,
			p.size,
			frame,
			seq,
			idx,
			p.frames,
			p.allFrames)
		if p.frames == p.allFrames {
			// Received all data
			if err := ana.printPgn(msg, p.data[:p.size], writer); err != nil {
				return err
			}
			p.used = false
			p.frames = 0
		}
	}
	return nil
}

func (ana *Analyzer) printPgn(
	msg *common.RawMessage,
	data []byte,
	writer io.Writer,
) error {
	if msg == nil {
		return nil
	}
	pgn, err := ana.getMatchingPgn(msg.PGN, data)
	if err != nil {
		return err
	}
	if pgn == nil {
		return ana.Logger.Abort("No PGN definition found for PGN %d\n", msg.PGN)
	}

	if ana.ShowData {
		f := ana.OutFile

		if ana.ShowJSON {
			f = ana.OutErrFile
		}

		fmt.Fprintf(f, "%s %d %3d %3d %6d %s: ", msg.Timestamp, msg.Prio, msg.Src, msg.Dst, msg.PGN, pgn.description)
		for i := 0; i < len(data); i++ {
			fmt.Fprintf(f, " %2.02X", data[i])
		}
		fmt.Fprint(f, '\n')

		fmt.Fprintf(f, "%s %d %3d %3d %6d %s: ", msg.Timestamp, msg.Prio, msg.Src, msg.Dst, msg.PGN, pgn.description)
		for i := 0; i < len(data); i++ {
			char := '.'
			if unicode.IsNumber(rune(data[i])) || unicode.IsLetter(rune(data[i])) {
				char = rune(data[i])
			}
			fmt.Fprintf(f, "  %c", char)
		}
		fmt.Fprint(f, '\n')
	}
	if ana.ShowJSON {
		if pgn.camelDescription != "" {
			ana.pb.Printf("\"%s\":", pgn.camelDescription)
		}
		ana.pb.Printf("{\"timestamp\":\"%s\",\"prio\":%d,\"src\":%d,\"dst\":%d,\"pgn\":%d,\"description\":\"%s\"",
			msg.Timestamp,
			msg.Prio,
			msg.Src,
			msg.Dst,
			msg.PGN,
			pgn.description)
		ana.closingBraces = "}"
		ana.sep = ",\"fields\":{"
	} else {
		ana.pb.Printf("%s %d %3d %3d %6d %s:", msg.Timestamp, msg.Prio, msg.Src, msg.Dst, msg.PGN, pgn.description)
		ana.sep = " "
	}

	ana.Logger.Debug("fieldCount=%d repeatingStart1=%d\n", pgn.fieldCount, pgn.repeatingStart1)

	ana.variableFieldRepeat[0] = 255 // Can be overridden by '# of parameters'
	ana.variableFieldRepeat[1] = 0   // Can be overridden by '# of parameters'
	repetition := 0
	variableFields := int64(0)
	r := true

	startBit := 0
	variableFieldStart := 0
	variableFieldCount := 0
	for i := 0; (startBit >> 3) < len(data); i++ {
		field := &pgn.fieldList[i]

		if variableFields == 0 {
			repetition = 0
		}

		if pgn.repeatingCount1 > 0 && field.order == pgn.repeatingStart1 && repetition == 0 {
			if ana.ShowJSON {
				sep, err := ana.getSep()
				if err != nil {
					return err
				}
				ana.pb.Printf("%s\"list\":[{", sep)
				ana.closingBraces += "]}"
				ana.sep = ""
			}
			// Only now is ana.variableFieldRepeat set
			variableFields = int64(pgn.repeatingCount1) * ana.variableFieldRepeat[0]
			variableFieldCount = int(pgn.repeatingCount1)
			variableFieldStart = int(pgn.repeatingStart1)
			repetition = 1
		}
		if pgn.repeatingCount2 > 0 && field.order == pgn.repeatingStart2 && repetition == 0 {
			if ana.ShowJSON {
				ana.pb.Printf("}],\"list2\":[{")
				ana.sep = ""
			}
			// Only now is ana.variableFieldRepeat set
			variableFields = int64(pgn.repeatingCount2) * ana.variableFieldRepeat[1]
			variableFieldCount = int(pgn.repeatingCount2)
			variableFieldStart = int(pgn.repeatingStart2)
			repetition = 1
		}

		if variableFields > 0 {
			if i+1 == variableFieldStart+variableFieldCount {
				i = variableFieldStart - 1
				field = &pgn.fieldList[i]
				repetition++
				if ana.ShowJSON {
					ana.pb.Printf("},{")
					ana.sep = ""
				}
			}
			ana.Logger.Debug("variableFields: repetition=%d field=%d variableFieldStart=%d variableFieldCount=%d remaining=%d\n",
				repetition,
				i+1,
				variableFieldStart,
				variableFieldCount,
				variableFields)
			variableFields--
		}

		if field.camelName == "" && field.name == "" {
			ana.Logger.Debug("PGN %d has unknown bytes at end: %d\n", msg.PGN, len(data)-(startBit>>3))
			break
		}

		fieldName := field.name
		if field.camelName != "" {
			fieldName = field.camelName
		}
		if repetition >= 1 && !ana.ShowJSON {
			if field.camelName != "" {
				fieldName += "_"
			} else {
				fieldName += " "
			}
			fieldName = fmt.Sprintf("%s%d", fieldName, repetition)
		}

		var countBits int
		if ok, err := ana.printField(field, fieldName, data, startBit, &countBits); err != nil {
			return err
		} else if !ok {
			r = false
			break
		}

		startBit += countBits
	}

	if ana.ShowJSON {
		for i := len(ana.closingBraces); i != 0; {
			i--
			ana.pb.Printf("%c", ana.closingBraces[i])
		}
	}
	ana.pb.Printf("\n")

	if r {
		ana.pb.Write(writer)
		if variableFields > 0 && ana.variableFieldRepeat[0] < math.MaxUint8 {
			//nolint:errcheck
			ana.Logger.Error("PGN %d has %d missing fields in repeating set\n", msg.PGN, variableFields)
		}
	} else {
		if !ana.ShowJSON {
			ana.pb.Write(writer)
		}
		ana.pb.Reset()
		//nolint:errcheck
		ana.Logger.Error("PGN %d analysis error\n", msg.PGN)
	}

	if msg.PGN == 126992 && ana.currentDate < math.MaxUint16 && ana.currentTime < math.MaxUint32 && ana.ClockSrc == int64(msg.Src) {
		//nolint:errcheck
		ana.Logger.Error("WILL NOT SETSYSTEMCLOCK FOR 126992")
	}
	return nil
}

const maxBraces = 15

func (ana *Analyzer) getSep() (string, error) {
	s := ana.sep

	if ana.ShowJSON {
		ana.sep = ","
		if strings.Contains(s, "{") {
			if len(ana.closingBraces) >= maxBraces-1 {
				return "", ana.Logger.Error("Too many braces\n")
			}
			ana.closingBraces += "}"
		}
	} else {
		ana.sep = ";"
	}

	return s, nil
}

func (ana *Analyzer) fillGlobalsBasedOnFieldName(
	fieldName string,
	data []byte,
	startBit int,
	bits int,
) {
	var value int64
	var maxValue int64

	if fieldName == "PGN" {
		extractNumber(nil, data, startBit, bits, &value, &maxValue, ana.Logger)
		ana.Logger.Debug("Reference PGN = %d\n", value)
		ana.refPgn = value
		return
	}

	if fieldName == "Length" {
		extractNumber(nil, data, startBit, bits, &value, &maxValue, ana.Logger)
		ana.Logger.Debug("for next field: length = %d\n", value)
		ana.length = value
		return
	}
}

func (ana *Analyzer) printField(
	field *pgnField,
	fieldName string,
	data []byte,
	startBit int,
	bits *int,
) (bool, error) {
	if fieldName == "" {
		if field.camelName != "" {
			fieldName = field.camelName
		} else {
			fieldName = field.name
		}
	}

	resolution := field.resolution
	if resolution == 0.0 {
		resolution = field.ft.resolution
	}

	ana.Logger.Debug("PGN %d: printField(<%s>, \"%s\", ..., startBit=%d) resolution=%g\n",
		field.pgn.pgn,
		field.name,
		fieldName,
		startBit,
		resolution)

	var bytes int
	if field.size != 0 || field.ft != nil {
		if field.size != 0 {
			*bits = int(field.size)
		} else {
			*bits = int(field.ft.size)
		}
		bytes = (*bits + 7) / 8
		bytes = common.Min(bytes, len(data)-startBit/8)
		*bits = common.Min(bytes*8, *bits)
	} else {
		*bits = 0
	}

	ana.fillGlobalsBasedOnFieldName(field.name, data, startBit, *bits)

	ana.Logger.Debug("PGN %d: printField <%s>, \"%s\": bits=%d proprietary=%t refPgn=%d\n",
		field.pgn.pgn,
		field.name,
		fieldName,
		*bits,
		field.proprietary,
		ana.refPgn)

	if field.proprietary {
		if (ana.refPgn >= 65280 && ana.refPgn <= 65535) ||
			(ana.refPgn >= 126720 && ana.refPgn <= 126975) ||
			(ana.refPgn >= 130816 && ana.refPgn <= 131071) {
			// proprietary, allow field
		} else {
			// standard PGN, skip field
			*bits = 0
			return true, nil
		}
	}

	var oldClosingBracesLen int
	if field.ft != nil && field.ft.pf != nil {
		location := ana.pb.Location()
		oldSep := ana.sep
		oldClosingBracesLen = len(ana.closingBraces)
		location2 := 0

		if !field.ft.pfIsPrintVariable {
			sep, err := ana.getSep()
			if err != nil {
				return false, err
			}
			if ana.ShowJSON {
				ana.pb.Printf("%s\"%s\":", sep, fieldName)
				ana.sep = ","
				if ana.ShowBytes || ana.ShowJSONValue {
					location2 = ana.pb.Location()
				}
			} else {
				ana.pb.Printf("%s %s = ", sep, fieldName)
				ana.sep = ";"
			}
		}
		location3 := ana.pb.Location()
		ana.Logger.Debug(
			"PGN %d: printField <%s>, \"%s\": calling function for %s\n", field.pgn.pgn, field.name, fieldName, field.fieldType)
		ana.skip = false
		var err error
		r, err := field.ft.pf(ana, field, fieldName, data, startBit, bits)
		if err != nil {
			return false, err
		}
		// if match fails, r == false. If field is not printed, ana.skip == true
		ana.Logger.Debug("PGN %d: printField <%s>, \"%s\": result %t bits=%d\n", field.pgn.pgn, field.name, fieldName, r, *bits)
		if r && !ana.skip {
			if location3 == ana.pb.Location() && !ana.ShowBytes {
				//nolint:errcheck
				ana.Logger.Error("PGN %d: field \"%s\" print routine did not print anything\n", field.pgn.pgn, field.name)
				r = false
			} else if ana.ShowBytes && !field.ft.pfIsPrintVariable {
				location3 = ana.pb.Location()
				if ana.pb.Chr(location3-1) == '}' {
					ana.pb.Set(location3 - 1)
				}
				ana.showBytesOrBits(data[startBit>>3:], startBit&7, *bits)
				if ana.ShowJSON {
					ana.pb.Printf("}")
				}
			}
			if location2 != 0 {
				location3 = ana.pb.Location()
				if ana.pb.Chr(location3-1) == '}' {
					// Prepend {"value":
					ana.pb.Insert(location2, "{\"value\":")
				}
			}
		}
		if !r || ana.skip {
			ana.pb.Set(location)
			ana.sep = oldSep
			ana.closingBraces = ana.closingBraces[:oldClosingBracesLen]
		}
		return r, nil
	}
	//nolint:errcheck
	ana.Logger.Error("PGN %d: no function found to print field '%s'\n", field.pgn.pgn, fieldName)
	return false, nil
}

func (ana *Analyzer) showBytesOrBits(data []byte, startBit, bits int) {
	if ana.ShowJSON {
		location := ana.pb.Location()

		if location == 0 || ana.pb.Chr(location-1) != '{' {
			ana.pb.Printf(",")
		}
		ana.pb.Printf("\"bytes\":\"")
	} else {
		ana.pb.Printf(" (bytes = \"")
	}
	remainingBits := bits
	s := ""
	for i := 0; i < (bits+7)>>3; i++ {
		byteData := data[i]

		if i == 0 && startBit != 0 {
			byteData >>= startBit // Shift off older bits
			if remainingBits+startBit < 8 {
				byteData &= ((1 << remainingBits) - 1)
			}
			byteData <<= startBit // Shift zeros back in
			remainingBits -= (8 - startBit)
		} else {
			if remainingBits < 8 {
				// only the lower remainingBits should be used
				byteData &= ((1 << remainingBits) - 1)
			}
			remainingBits -= 8
		}
		ana.pb.Printf("%s%2.02X", s, byteData)
		s = " "
	}
	ana.pb.Printf("\"")

	var value int64
	var maxValue int64

	if startBit != 0 || ((bits & 7) != 0) {
		extractNumber(nil, data, startBit, bits, &value, &maxValue, ana.Logger)
		if ana.ShowJSON {
			ana.pb.Printf(",\"bits\":\"")
		} else {
			ana.pb.Printf(", bits = \"")
		}

		for i := bits; i > 0; {
			i--
			byteData := (value >> (i >> 3)) & 0xff
			char := '0'
			if (byteData & (1 << (i & 7))) != 0 {
				char = '1'
			}
			ana.pb.Printf("%c", char)
		}
		ana.pb.Printf("\"")
	}

	if !ana.ShowJSON {
		ana.pb.Printf(")")
	}
}

func fieldPrintVariable(
	ana *Analyzer,
	_ *pgnField,
	fieldName string,
	data []byte,
	startBit int,
	bits *int,
) (bool, error) {
	refField := ana.getField(uint32(ana.refPgn), uint32(data[startBit/8-1]-1))
	if refField != nil {
		ana.Logger.Debug("Field %s: found variable field %d '%s'\n", fieldName, ana.refPgn, refField.name)
		r, err := ana.printField(refField, fieldName, data, startBit, bits)
		if err != nil {
			return false, err
		}
		*bits = (*bits + 7) & ^0x07 // round to bytes
		return r, nil
	}

	//nolint:errcheck
	ana.Logger.Error("Field %s: cannot derive variable length for PGN %d field # %d\n", fieldName, ana.refPgn, data[len(data)-1])
	*bits = 8 /* Gotta assume something */
	return false, nil
}

func (ana *Analyzer) printCanRaw(msg *common.RawMessage) {
	f := ana.OutFile

	if ana.OnlySrc >= 0 && ana.OnlySrc != int64(msg.Src) {
		return
	}
	if ana.OnlyDst >= 0 && ana.OnlyDst != int64(msg.Dst) {
		return
	}
	if ana.OnlyPgn > 0 && ana.OnlyPgn != int64(msg.PGN) {
		return
	}

	if ana.ShowJSON {
		f = ana.OutErrFile
	}

	if ana.ShowRaw && (ana.OnlyPgn != 0 || ana.OnlyPgn == int64(msg.PGN)) {
		fmt.Fprintf(f, "%s,%d,%d,%d,%d,%d", msg.Timestamp, msg.Prio, msg.PGN, msg.Src, msg.Dst, msg.Len)
		for i := uint8(0); i < msg.Len; i++ {
			fmt.Fprintf(f, ",%02x", msg.Data[i])
		}
		fmt.Fprintf(f, "\n")
	}
}
