package kurz

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.bug.st/serial"
)

var (
	kurzBaudRate                   int
	kurzDataBits                   int
	kurzCmdListSerialDeviceByID    string
	kurzRegexSensorSerialUSBPrefix string
	kurzDefaultPortFormat          string
	kurzRegexSensorModel           string
	kurzRegexSensorSerialNumber    string
	kurzRegexSensorSoftwareVersion string
)

func init() {
	kurzBaudRate = 9600
	kurzDataBits = 8
	kurzCmdListSerialDeviceByID = "ls -l /dev/serial/by-id"
	kurzRegexSensorSerialUSBPrefix = "usb-FTDI_.*_USB.*->.*ttyUSB\\d+"
	kurzDefaultPortFormat = "/dev/%s"
	kurzRegexSensorModel = "Device\\s*:\\s*\\w*"
	kurzRegexSensorSerialNumber = "SNUM\\s*:\\s*\\w*"
	kurzRegexSensorSoftwareVersion = "SW version\\s*:\\s*\\d.\\d.\\d"
}

type KurzSensor struct {
	baudRate              int
	dataBits              int
	serialConn            serial.Port
	flowCh                chan float64
	lock                  sync.Mutex
	sensorModel           string
	sensorSerialNumber    string
	sensorSoftwareVersion string
	constantFlowRateSCFM  float64
}

func newKurzSensor(baudRate int) (*KurzSensor, error) {
	constantFlowRateSCFM := 0.0
	if val := os.Getenv("CONSTANT_FLOW_RATE_SCFM"); val != "" {
		if rate, err := strconv.ParseFloat(val, 64); err == nil {
			constantFlowRateSCFM = rate
		}
	}

	return &KurzSensor{
		baudRate:             baudRate,
		dataBits:             kurzDataBits,
		flowCh:               make(chan float64),
		constantFlowRateSCFM: constantFlowRateSCFM,
	}, nil
}

func (ks *KurzSensor) searchPorts() (string, error) {
	log.Printf("searching for Kurz sensor")

	output, err := exec.Command("sh", "-c", kurzCmdListSerialDeviceByID).Output()
	if err != nil {
		return "", fmt.Errorf("failed to execute command: %v", err)
	}
	log.Printf("command output: %s", string(output))
	log.Printf("using regex pattern: %s", kurzRegexSensorSerialUSBPrefix)

	match := regexp.MustCompile(kurzRegexSensorSerialUSBPrefix).FindStringSubmatch(string(output))
	log.Printf("regex results: %v", match)
	if len(match) == 0 {
		log.Println("no matches found for the Kurz sensor regex.")
		return "", fmt.Errorf("kurz sensor not found")
	}

	parts := strings.Fields(match[0])
	if len(parts) > 0 {
		sensorPath := parts[len(parts)-1]
		if strings.Contains(sensorPath, "/") {
			lastPart := strings.Split(sensorPath, "/")[len(strings.Split(sensorPath, "/"))-1]
			log.Printf("kurzDefaultPortFormat: %s, last part of sensorPath: %s", kurzDefaultPortFormat, lastPart)

			port := fmt.Sprintf(kurzDefaultPortFormat, strings.Split(sensorPath, "/")[len(strings.Split(sensorPath, "/"))-1])
			log.Printf("kurz sensor found on port: %s", port)
			return port, nil
		}
	}

	log.Println("kurz sensor detected but no valid port found.")

	return "", fmt.Errorf("kurz sensor not found")
}

func (ks *KurzSensor) openSerialConnection() error {
	if ks.serialConn != nil {
		err := ks.serialConn.Close()
		if err != nil {
			log.Printf("Error closing existing serial connection: %v", err)
		}
	}

	port, err := ks.searchPorts()
	if err != nil {
		return fmt.Errorf("failed to find Kurz sensor: %v", err)
	}
	log.Printf("found Kurz sensor at port: %s", port)

	mode := &serial.Mode{
		BaudRate: ks.baudRate,
		DataBits: ks.dataBits,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
	}

	ks.serialConn, err = serial.Open(port, mode)
	if err != nil {
		return fmt.Errorf("failed to open serial connection: %v", err)
	}

	log.Printf("opened serial connection")

	err = ks.collectSensorInfo()
	if err != nil {
		return fmt.Errorf("failed to collect sensor information: %v", err)
	}

	return nil
}

func (ks *KurzSensor) collectSensorInfo() error { // send specific commands to Kurz to retrieve sensor info, parsed w/regex and values stored
	err := ks.writeCommand("?")
	if err != nil {
		return err
	}

	reader := bufio.NewReader(ks.serialConn)
	response, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read sensor info response: %v", err)
	}

	sensorModel := regexp.MustCompile(kurzRegexSensorModel).FindStringSubmatch(response)
	if len(sensorModel) > 1 {
		ks.sensorModel = sensorModel[1]
	}

	sensorSerialNumber := regexp.MustCompile(kurzRegexSensorSerialNumber).FindStringSubmatch(response)
	if len(sensorSerialNumber) > 1 {
		ks.sensorSerialNumber = sensorSerialNumber[1]
	}

	sensorSoftwareVersion := regexp.MustCompile(kurzRegexSensorSoftwareVersion).FindStringSubmatch(response)
	if len(sensorSoftwareVersion) > 1 {
		ks.sensorSoftwareVersion = sensorSoftwareVersion[1]
	}

	return nil
}

func (ks *KurzSensor) writeCommand(command string) error {
	_, err := ks.serialConn.Write([]byte(command))
	if err != nil {
		return fmt.Errorf("failed to write command: %v", err)
	}
	return nil
}

func (ks *KurzSensor) readFlowRate() (float64, error) {
	if ks.constantFlowRateSCFM != 0.0 { // if the constantFlowRateSCFM field is not 0, it means the env var is set and parsed and we can directly return it
		return ks.constantFlowRateSCFM, nil // instead of interacting with the physical flow meter
	}

	ks.lock.Lock()
	defer ks.lock.Unlock()

	err := ks.writeCommand("x")
	if err != nil {
		return 0, err
	}

	reader := bufio.NewReader(ks.serialConn)
	response, err := reader.ReadString('\n')
	if err != nil {
		return 0, fmt.Errorf("failed to read response: %v", err)
	}

	parts := strings.Fields(response)
	if len(parts) < 4 {
		return 0, fmt.Errorf("invalid response format")
	}

	flowRate, err := strconv.ParseFloat(parts[3], 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse flow rate: %v", err)
	}

	return flowRate, nil
}

func (ks *KurzSensor) startKurzSensor() {
	for {
		flowRate, err := ks.readFlowRate()
		if err != nil {
			log.Printf("failed to read flow rate: %v", err)
			time.Sleep(time.Second)
			continue
		}
		ks.flowCh <- flowRate
	}
}

func (ks *KurzSensor) close() error {
	return ks.serialConn.Close()
}
