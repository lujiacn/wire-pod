package logger

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	debugLogging bool
	LogList      string
	LogArray     []string
	LogTrayList  string
	LogTrayArray []string
	LogTrayChan  chan string
	logMutex     sync.Mutex
	maxLogSize   = 1000 // Maximum size of a single log entry in bytes
	maxLogs      = 200  // Maximum number of logs to keep in LogTrayArray
	maxUILogs    = 50   // Maximum number of logs to keep in LogArray
)

func GetLogTrayChan() chan string {
	return LogTrayChan
}

func Init() {
	LogTrayChan = make(chan string, 100) // Buffered channel
	if os.Getenv("DEBUG_LOGGING") == "true" {
		debugLogging = true
	} else {
		debugLogging = false
	}
	LogArray = make([]string, 0, maxUILogs)
	LogTrayArray = make([]string, 0, maxLogs)
}

func Println(a ...any) {
	LogTray(a...)
	if debugLogging {
		fmt.Println(a...)
	}
}

func LogUI(a ...any) {
	logMutex.Lock()
	defer logMutex.Unlock()

	logEntry := time.Now().Format("2006.01.02 15:04:05") + ": " + fmt.Sprint(a...) + "\n"
	if len(logEntry) > maxLogSize {
		logEntry = logEntry[:maxLogSize] + "...\n"
	}

	LogArray = append(LogArray, logEntry)
	if len(LogArray) > maxUILogs {
		LogArray = LogArray[len(LogArray)-maxUILogs:]
	}

	var builder strings.Builder
	for _, b := range LogArray {
		builder.WriteString(b)
	}
	LogList = builder.String()
}

func LogTray(a ...any) {
	logMutex.Lock()
	defer logMutex.Unlock()

	logEntry := time.Now().Format("2006.01.02 15:04:05") + ": " + fmt.Sprint(a...) + "\n"
	if len(logEntry) > maxLogSize {
		logEntry = logEntry[:maxLogSize] + "...\n"
	}

	LogTrayArray = append(LogTrayArray, logEntry)
	if len(LogTrayArray) > maxLogs {
		LogTrayArray = LogTrayArray[len(LogTrayArray)-maxLogs:]
	}

	var builder strings.Builder
	for _, b := range LogTrayArray {
		builder.WriteString(b)
	}
	LogTrayList = builder.String()

	select {
	case LogTrayChan <- logEntry:
	default:
		// Channel is full, log is discarded
	}
}
