// Copyright (C) 2010, Kyle Lemons <kyle@kylelemons.net>.  All rights reserved.

// Package log4go provides level-based and highly configurable logging.
//
// Enhanced Logging
//
// This is inspired by the logging functionality in Java.  Essentially, you create a Logger
// object and create output filters for it.  You can send whatever you want to the Logger,
// and it will filter that based on your settings and send it to the outputs.  This way, you
// can put as much debug code in your program as you want, and when you're done you can filter
// out the mundane messages so only the important ones show up.
//
// Utility functions are provided to make life easier. Here is some example code to get started:
//
// log := log4go.NewLogger()
// log.AddFilter("stdout", log4go.DEBUG, log4go.NewConsoleLogWriter())
// log.AddFilter("log",    log4go.FINE,  log4go.NewFileLogWriter("example.log", true))
// log.Info("The time is now: %s", time.LocalTime().Format("15:04:05 MST 2006/01/02"))
//
// The first two lines can be combined with the utility NewDefaultLogger:
//
// log := log4go.NewDefaultLogger(log4go.DEBUG)
// log.AddFilter("log",    log4go.FINE,  log4go.NewFileLogWriter("example.log", true))
// log.Info("The time is now: %s", time.LocalTime().Format("15:04:05 MST 2006/01/02"))
//
// Usage notes:
// - The ConsoleLogWriter does not display the source of the message to standard
//   output, but the FileLogWriter does.
// - The utility functions (Info, Debug, Warn, etc) derive their source from the
//   calling function, and this incurs extra overhead.
//
// Changes from 2.0:
// - The external interface has remained mostly stable, but a lot of the
//   internals have been changed, so if you depended on any of this or created
//   your own LogWriter, then you will probably have to update your code.  In
//   particular, Logger is now a map and ConsoleLogWriter is now a channel
//   behind-the-scenes, and the LogWrite method no longer has return values.
//
// Future work: (please let me know if you think I should work on any of these particularly)
// - Log file rotation
// - Logging configuration files ala log4j
// - Have the ability to remove filters?
// - Have GetInfoChannel, GetDebugChannel, etc return a chan string that allows
//   for another method of logging
// - Add an XML filter type
package log4go

import (
	"errors"
	"fmt"
	"os"
    "path/filepath"
	"runtime"
	"strings"
	"time"
	"encoding/json"
)

// Version information
const (
	L4G_VERSION = "log4go-v4.0.0"
	L4G_MAJOR   = 4
	L4G_MINOR   = 0
	L4G_BUILD   = 0
)

/****** Constants ******/

// These are the integer logging levels used by the logger
type Level int

const (
	FINEST Level = iota
	FINE
	DEBUG
	TRACE
	INFO
	WARNING
	ERROR
	CRITICAL
)

// Default level passed to runtime.Caller
const DefaultFileDepth int = 2

// Logging level strings
var (
	levelStrings = [...]string{"FNST", "FINE", "DEBG", "TRAC", "INFO", "WARN", "EROR", "CRIT"}
)

func (l Level) String() string {
	if l < 0 || int(l) > len(levelStrings) {
		return "UNKNOWN"
	}
	return levelStrings[int(l)]
}

/****** Variables ******/
var (
	// LogBufferLength specifies how many log messages a particular log4go
	// logger can buffer at a time before writing them.
	LogBufferLength = 32
)

/****** LogRecord ******/

// A LogRecord contains all of the pertinent information for each message
type LogRecord struct {
	Level   Level     // The log level
	Created time.Time // The time at which the log message was created (nanoseconds)
	Source  string    // The message source
	Message string    // The log message
}

/****** LogWriter ******/

// This is an interface for anything that should be able to write logs
type LogWriter interface {
	// This will be called to log a LogRecord message.
	LogWrite(rec *LogRecord)

	// This should clean up anything lingering about the LogWriter, as it is called before
	// the LogWriter is removed.  LogWrite should not be called after Close.
	Close()
}

/****** Logger ******/

// A Filter represents the log level below which no log records are written to
// the associated LogWriter.
type Filter struct {
	Level Level

	rec 	chan *LogRecord	// write queue
    closeq 	chan struct{}	// closed when user requests close
	closing bool	// true if Socket was closed at API level

	LogWriter
}

func NewFilter(lvl Level, writer LogWriter) *Filter {
	f := &Filter {
		rec: 		make(chan *LogRecord, LogBufferLength),
	    closeq:		make(chan struct{}),
		closing: 	false,
		
		Level:		lvl,
		LogWriter:	writer,
	}
	
	go f.run()
	return f
}
	
func (f *Filter) WriteToChan(rec *LogRecord) {
	if f.closing {
		fmt.Fprintf(os.Stderr, "LogWriter: channel has been closed. Message is [%s]\n", rec.Message)
		return
	}
	f.rec <- rec
}

func (f *Filter) run() {
	for {
		select {
		case <-f.closeq:
			return
		case rec, ok := <-f.rec:
			if !ok {
				return
			}
			f.LogWrite(rec)
		}
	}
}

func (f *Filter) Close() {
	if f.closing {
		return
	}
	f.closing = true

	close(f.closeq)

	defer f.LogWriter.Close()

	close(f.rec)
	if len(f.rec) <= 0 {
		return
	}
	// drain the log channel before closing
	for rec := range f.rec {
		f.LogWrite(rec)
	}
}

// A Logger represents a collection of Filters through which log messages are
// written.
type Logger map[string]*Filter

// Create a new logger.
//
// DEPRECATED: Use make(Logger) instead.
func NewLogger() Logger {
	os.Stderr.WriteString("warning: use of deprecated NewLogger\n")
	return make(Logger)
}

// Create a new logger with a "stdout" filter configured to send log messages at
// or above lvl to standard output.
//
// DEPRECATED: use NewDefaultLogger instead.
func NewConsoleLogger(lvl Level) Logger {
	os.Stderr.WriteString("warning: use of deprecated NewConsoleLogger\n")
	return Logger{
		"stdout": NewFilter(lvl, NewConsoleLogWriter()),
	}
}

// Create a new logger with a "stdout" filter configured to send log messages at
// or above lvl to standard output.
func NewDefaultLogger(lvl Level) Logger {
	return Logger{
		"stdout": NewFilter(lvl, NewConsoleLogWriter()),
	}
}

// Closes all log writers in preparation for exiting the program or a
// reconfiguration of logging.  Calling this is not really imperative, unless
// you want to guarantee that all log messages are written.  Close removes
// all filters (and thus all LogWriters) from the logger.
func (log Logger) Close() {
	// Close all open loggers
	for name, filt := range log {
		filt.Close()
		delete(log, name)
	}
}

// Add a new LogWriter to the Logger which will only log messages at lvl or
// higher.  This function should not be called from multiple goroutines.
// Returns the logger for chaining.
func (log Logger) AddFilter(name string, lvl Level, writer LogWriter) Logger {
	log[name] = NewFilter(lvl, writer)
	return log
}

/******* Logging *******/

// Determine if any logging will be done
func (log Logger) skip(lvl Level) bool {
	for _, filt := range log {
		if lvl >= filt.Level {
			return false
		}
	}
	return true
}

// Dispatch the logs
func (log Logger) dispatch(rec *LogRecord) {
	for _, filt := range log {
		if rec.Level < filt.Level {
			continue
		}
		filt.WriteToChan(rec)
	}
}

// Send a formatted log message internally
func (log Logger) intLogf(lvl Level, format string, args ...interface{}) {
	if log.skip(lvl) {
		return
	}

	// Determine caller func
	pc, _, lineno, ok := runtime.Caller(DefaultFileDepth)
	src := ""
	if ok {
		src = fmt.Sprintf("%s:%d", filepath.Base(runtime.FuncForPC(pc).Name()), lineno)
	}

	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}

	// Make the log record
	rec := &LogRecord{
		Level:   lvl,
		Created: time.Now(),
		Source:  src,
		Message: msg,
	}

	log.dispatch(rec)
}

// Send a closure log message internally
func (log Logger) intLogc(lvl Level, closure func() string) {
	if log.skip(lvl) {
		return
	}

	// Determine caller func
	pc, _, lineno, ok := runtime.Caller(DefaultFileDepth)
	src := ""
	if ok {
		src = fmt.Sprintf("%s:%d", filepath.Base(runtime.FuncForPC(pc).Name()), lineno)
	}

	// Make the log record
	rec := &LogRecord{
		Level:   lvl,
		Created: time.Now(),
		Source:  src,
		Message: closure(),
	}

	log.dispatch(rec)
}

// Send a log message with manual level, source, and message.
func (log Logger) Log(lvl Level, source, message string) {
	if log.skip(lvl) {
		return
	}

	// Make the log record
	rec := &LogRecord{
		Level:   lvl,
		Created: time.Now(),
		Source:  source,
		Message: message,
	}

	log.dispatch(rec)
}

// Send a log message with manual level, source, and message.
func (log Logger) Json(data []byte) {
	var rec LogRecord
	
	// Make the log record
	err := json.Unmarshal(data, &rec)
	if err != nil {
		// log to standard output
		msg := "Err: " + err.Error() + " - " + string(data[0:])
		log.intLogf(WARNING, msg)
		return
	}
	
	if log.skip(rec.Level) {
		return
	}

	log.dispatch(&rec)
}

// Logf logs a formatted log message at the given log level, using the caller as
// its source.
func (log Logger) Logf(lvl Level, format string, args ...interface{}) {
	log.intLogf(lvl, format, args...)
}

// Logc logs a string returned by the closure at the given log level, using the caller as
// its source.  If no log message would be written, the closure is never called.
func (log Logger) Logc(lvl Level, closure func() string) {
	log.intLogc(lvl, closure)
}

// Finest logs a message at the finest log level.
// See Debug for an explanation of the arguments.
func (log Logger) Finest(arg0 interface{}, args ...interface{}) {
	const (
		lvl = FINEST
	)
	switch first := arg0.(type) {
	case string:
		// Use the string as a format string
		log.intLogf(lvl, first, args...)
	case func() string:
		// Log the closure (no other arguments used)
		log.intLogc(lvl, first)
	default:
		// Build a format string so that it will be similar to Sprint
		log.intLogf(lvl, fmt.Sprint(arg0)+strings.Repeat(" %v", len(args)), args...)
	}
}

// Fine logs a message at the fine log level.
// See Debug for an explanation of the arguments.
func (log Logger) Fine(arg0 interface{}, args ...interface{}) {
	const (
		lvl = FINE
	)
	switch first := arg0.(type) {
	case string:
		// Use the string as a format string
		log.intLogf(lvl, first, args...)
	case func() string:
		// Log the closure (no other arguments used)
		log.intLogc(lvl, first)
	default:
		// Build a format string so that it will be similar to Sprint
		log.intLogf(lvl, fmt.Sprint(arg0)+strings.Repeat(" %v", len(args)), args...)
	}
}

// Debug is a utility method for debug log messages.
// The behavior of Debug depends on the first argument:
// - arg0 is a string
//   When given a string as the first argument, this behaves like Logf but with
//   the DEBUG log level: the first argument is interpreted as a format for the
//   latter arguments.
// - arg0 is a func()string
//   When given a closure of type func()string, this logs the string returned by
//   the closure iff it will be logged.  The closure runs at most one time.
// - arg0 is interface{}
//   When given anything else, the log message will be each of the arguments
//   formatted with %v and separated by spaces (ala Sprint).
func (log Logger) Debug(arg0 interface{}, args ...interface{}) {
	const (
		lvl = DEBUG
	)
	switch first := arg0.(type) {
	case string:
		// Use the string as a format string
		log.intLogf(lvl, first, args...)
	case func() string:
		// Log the closure (no other arguments used)
		log.intLogc(lvl, first)
	default:
		// Build a format string so that it will be similar to Sprint
		log.intLogf(lvl, fmt.Sprint(arg0)+strings.Repeat(" %v", len(args)), args...)
	}
}

// Trace logs a message at the trace log level.
// See Debug for an explanation of the arguments.
func (log Logger) Trace(arg0 interface{}, args ...interface{}) {
	const (
		lvl = TRACE
	)
	switch first := arg0.(type) {
	case string:
		// Use the string as a format string
		log.intLogf(lvl, first, args...)
	case func() string:
		// Log the closure (no other arguments used)
		log.intLogc(lvl, first)
	default:
		// Build a format string so that it will be similar to Sprint
		log.intLogf(lvl, fmt.Sprint(arg0)+strings.Repeat(" %v", len(args)), args...)
	}
}

// Info logs a message at the info log level.
// See Debug for an explanation of the arguments.
func (log Logger) Info(arg0 interface{}, args ...interface{}) {
	const (
		lvl = INFO
	)
	switch first := arg0.(type) {
	case string:
		// Use the string as a format string
		log.intLogf(lvl, first, args...)
	case func() string:
		// Log the closure (no other arguments used)
		log.intLogc(lvl, first)
	default:
		// Build a format string so that it will be similar to Sprint
		log.intLogf(lvl, fmt.Sprint(arg0)+strings.Repeat(" %v", len(args)), args...)
	}
}

// Warn logs a message at the warning log level and returns the formatted error.
// At the warning level and higher, there is no performance benefit if the
// message is not actually logged, because all formats are processed and all
// closures are executed to format the error message.
// See Debug for further explanation of the arguments.
func (log Logger) Warn(arg0 interface{}, args ...interface{}) error {
	const (
		lvl = WARNING
	)
	var msg string
	switch first := arg0.(type) {
	case string:
		// Use the string as a format string
		msg = fmt.Sprintf(first, args...)
	case func() string:
		// Log the closure (no other arguments used)
		msg = first()
	default:
		// Build a format string so that it will be similar to Sprint
		msg = fmt.Sprintf(fmt.Sprint(first)+strings.Repeat(" %v", len(args)), args...)
	}
	log.intLogf(lvl, msg)
	return errors.New(msg)
}

// Error logs a message at the error log level and returns the formatted error,
// See Warn for an explanation of the performance and Debug for an explanation
// of the parameters.
func (log Logger) Error(arg0 interface{}, args ...interface{}) error {
	const (
		lvl = ERROR
	)
	var msg string
	switch first := arg0.(type) {
	case string:
		// Use the string as a format string
		msg = fmt.Sprintf(first, args...)
	case func() string:
		// Log the closure (no other arguments used)
		msg = first()
	default:
		// Build a format string so that it will be similar to Sprint
		msg = fmt.Sprintf(fmt.Sprint(first)+strings.Repeat(" %v", len(args)), args...)
	}
	log.intLogf(lvl, msg)
	return errors.New(msg)
}

// Critical logs a message at the critical log level and returns the formatted error,
// See Warn for an explanation of the performance and Debug for an explanation
// of the parameters.
func (log Logger) Critical(arg0 interface{}, args ...interface{}) error {
	const (
		lvl = CRITICAL
	)
	var msg string
	switch first := arg0.(type) {
	case string:
		// Use the string as a format string
		msg = fmt.Sprintf(first, args...)
	case func() string:
		// Log the closure (no other arguments used)
		msg = first()
	default:
		// Build a format string so that it will be similar to Sprint
		msg = fmt.Sprintf(fmt.Sprint(first)+strings.Repeat(" %v", len(args)), args...)
	}
	log.intLogf(lvl, msg)
	return errors.New(msg)
}
