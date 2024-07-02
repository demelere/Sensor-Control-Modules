package vaisala

import (
    "bufio"
	"fmt"
    "log"
	"os/exec"
	"strings"
    "regexp"
    "strconv"
    "sync"
	"time"

    "go.bug.st/serial"
)

var (
	vaisalaBaudRate                   int
	vaisalaDefaultAddress             int
	vaisalaDataBits                   int
	vaisalaCmdListSerialDeviceByID    string
	vaisalaRegexSensorSerialUSBPrefix string
	vaisalaDefaultPortFormat          string
	vaisalaRegexSensorModel           string
	vaisalaRegexSensorSerialNumber    string
	vaisalaRegexSensorSoftwareVersion string
)

func init (
	vaisalaBaudRate = 19200
	vaisalaDefaultAddress = 240
	vaisalaDefaultPortFormat = "/dev/%s"
	vaisalaDataBits = 8
	vaisalaRegexSensorModel = "Device\\s+:\\s+(\\w+)"
	vaisalaRegexSensorSerialNumber = "SNUM\\s+:\\s+(\\w+)"
	vaisalaRegexSensorSoftwareVersion = "SW\\s+:\\s+(\\w+)"
	vaisalaRegexSensorSerialUSBPrefix = "usb-Silicon_Labs_Vaisala_USB.*->.*ttyUSB\\d+"
	vaisalaCmdListSerialDeviceByID = "ls -l /dev/serial/by-id"
)

type VaisalaSensor struct {
    baudRate              int
    dataBits              int
    defaultAddress        int
    serialConn            serial.Port
    co2Ch                 chan float64
    lock                  sync.Mutex
    sensorModel           string
    sensorSerialNumber    string
    sensorSoftwareVersion string
}

func init(){
	vaisalaBaudRate = 19200
	vaisalaDefaultAddress = 240
	vaisalaDefaultPortFormat = "/dev/%s"
	vaisalaDataBits = 8
	vaisalaRegexSensorModel = "Device\\s+:\\s+(\\w+)"
	vaisalaRegexSensorSerialNumber = "SNUM\\s+:\\s+(\\w+)"
	vaisalaRegexSensorSoftwareVersion = "SW\\s+:\\s+(\\w+)"
	vaisalaRegexSensorSerialUSBPrefix = "usb-Silicon_Labs_Vaisala_USB.*->.*ttyUSB\\d+"
	vaisalaCmdListSerialDeviceByID = "ls -l /dev/serial/by-id"
}

func newVaisalaSensor(baudRate int, defaultAddress int) (*VaisalaSensor, error) {
	return &VaisalaSensor{
		defaultAddress: defaultAddress,
		baudRate:       vaisalaBaudRate,
		dataBits:       vaisalaDataBits,
		co2Ch:          make(chan float64),
	}, nil
}

func (vs *VaisalaSensor) searchPorts() (string, error) {
	log.Printf("searching for Vaisala sensor")

	output, err := exec.Command("sh", "-c", vaisalaCmdListSerialDeviceByID).Output() // execute the shell cmd stored in vaisalaCmdListSerialDeviceByID and capture its output
	if err != nil {
		return "", fmt.Errorf("failed to execute command: %v", err)
	}
	log.Printf("command output: %s", string(output)) // command output: total 0
	// lrwxrwxrwx 1 root root 13 Jun  5 22:17 usb-Silicon_Labs_Vaisala_USB_Instrument_Cable_R3234317-if00-port0 -> ../../ttyUSB0
	log.Printf("using regex pattern: %s", vaisalaRegexSensorSerialUSBPrefix) // using regex pattern: usb-Silicon_Labs_Vaisala_USB.*->.*ttyUSB\d+

	match := regexp.MustCompile(vaisalaRegexSensorSerialUSBPrefix).FindStringSubmatch(string(output)) // compile the regex stored in vaisalaRegexSensorSerialUSBPrefix and find the first match in the cmd output
	log.Printf("regex results: %v", match)                                                            // regex results: [usb-Silicon_Labs_Vaisala_USB_Instrument_Cable_R3234317-if00-port0 -> ../../ttyUSB0]
	if len(match) == 0 {                                                                              // if no matches are found
		log.Println("no matches found for the Vaisala sensor regex.")
		return "", fmt.Errorf("vaisala sensor not found")
	}

	// or extract the part of the matched string
	parts := strings.Fields(match[0]) // split the first match into fields based on whitespace
	if len(parts) > 0 {               // check if there are any fields in the match,
		sensorPath := parts[len(parts)-1]      // and if so, assign the last field to sensorPath
		if strings.Contains(sensorPath, "/") { // check if sensorPath contains a fwd slash "/", and if it does
			// extract the last part of sensorPath
			lastPart := strings.Split(sensorPath, "/")[len(strings.Split(sensorPath, "/"))-1] // then assign the last part of it to lastPart
			log.Printf("vaisalaDefaultPortFormat: %s, last part of sensorPath: %s", vaisalaDefaultPortFormat, lastPart)

			port := fmt.Sprintf(vaisalaDefaultPortFormat, strings.Split(sensorPath, "/")[len(strings.Split(sensorPath, "/"))-1]) // format vaisalaDefaultPortFormat with the last part of sensorPath, and assign the result to port
			log.Printf("vaisala sensor found on port: %s", port)                                                                 // vaisala sensor found on port: /dev/{}%!(EXTRA string=ttyUSB0)
			return port, nil
		}
	}

	log.Println("vaisala sensor detected but no valid port found.")

	return "", fmt.Errorf("vaisala sensor not found")
}

func (vs *VaisalaSensor) openSerialConnection() error {
	if vs.serialConn != nil {
		err := vs.serialConn.Close()
		if err != nil {
			log.Printf("Error closing existing serial connection: %v", err)
			// handle the error, depending on whether I want to proceed with opening a new connection?
		}
	}

	port, err := vs.searchPorts()
	if err != nil {
		return fmt.Errorf("failed to find Vaisala sensor: %v", err)
	}
	log.Printf("found Vaisala sensor at port: %s", port)

	mode := &serial.Mode{
		BaudRate: vs.baudRate,
		DataBits: vs.dataBits,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
	}

	vs.serialConn, err = serial.Open(port, mode)
	if err != nil {
		return fmt.Errorf("failed to open serial connection: %v", err)
	}

	log.Printf("opened serial connection")

	_, err = vs.serialConn.Write([]byte(fmt.Sprintf("open %d\r\n", vs.defaultAddress)))
	if err != nil {
		return fmt.Errorf("failed to write open command: %v", err)
	}

	err = vs.collectProbeInfo()
	if err != nil {
		return fmt.Errorf("failed to collect probe information: %v", err)
	}

	return nil
}

func (vs *VaisalaSensor) collectProbeInfo() error {
	err := vs.writeCommand("?") //
	if err != nil {
		return err
	}

	reader := bufio.NewReader(vs.serialConn)
	response, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read probe info response: %v", err)
	}

	sensorModel := regexp.MustCompile(vaisalaRegexSensorModel).FindStringSubmatch(response)
	if len(sensorModel) > 1 {
		vs.sensorModel = sensorModel[1]
	}

	sensorSerialNumber := regexp.MustCompile(vaisalaRegexSensorSerialNumber).FindStringSubmatch(response)
	if len(sensorSerialNumber) > 1 {
		vs.sensorSerialNumber = sensorSerialNumber[1]
	}

	sensorSoftwareVersion := regexp.MustCompile(vaisalaRegexSensorSoftwareVersion).FindStringSubmatch(response)
	if len(sensorSoftwareVersion) > 1 {
		vs.sensorSoftwareVersion = sensorSoftwareVersion[1]
	}

	return nil
}

func (vs *VaisalaSensor) writeCommand(command string) error {
	_, err := vs.serialConn.Write([]byte(command + "\r\n")) // takes dynamic cmds instead of only hard-coded ones
	if err != nil {
		return fmt.Errorf("failed to write command: %v", err)
	}
	return nil
}

func (vs *VaisalaSensor) readCO2() (float64, error) {
	vs.lock.Lock()
	defer vs.lock.Unlock() // make sure only one goroutine can access this serial connection

	err := vs.writeCommand("send")
	if err != nil {
		return 0, err
	}

	reader := bufio.NewReader(vs.serialConn) // expect format "CO2=  400.00 ppm" ?
	response, err := reader.ReadString('\n')
	if err != nil {
		return 0, fmt.Errorf("failed to read response: %v", err)
	}

	parts := strings.Split(response, "=")

	if len(parts) < 2 {
		return 0, fmt.Errorf("invalid response format")
	}

	co2Value := strings.TrimSpace(parts[1])
	co2Parts := strings.Fields(co2Value)
	if len(co2Parts) < 1 {
		return 0, fmt.Errorf("failed to parse CO2 value from response")
	}
	co2, err := strconv.ParseFloat(co2Parts[0], 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse CO2 value: %v", err)
	}

	return co2, nil
}

func (vs *VaisalaSensor) startVaisalaSensor() {
	for {
		co2, err := vs.readCO2()
		if err != nil {
			log.Printf("failed to read CO2: %v", err)
			time.Sleep(time.Second)
			continue
		}
		vs.co2Ch <- co2
	}
}

func (vs *VaisalaSensor) close() error {
	return vs.serialConn.Close()
}