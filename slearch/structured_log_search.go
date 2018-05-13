package slearch

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"sync"

	"github.com/pkg/errors"
)

var (
	// Common errors
	ErrNoMatchingKeyValues   = errors.New("no matching key values found")
	ErrNoMatchingPrintValues = errors.New("no matching print values found")
)

func isSoftError(err error) bool {
	return err == ErrNoMatchingKeyValues || err == ErrNoMatchingPrintValues
}

type StructuredLogFormatter interface {
	GetValueFromLine(config Config, line []byte, key string) (string, error)
	FormatFoundValues(config Config, valuesFound []KV) string
}

func StructuredLoggingSearch(config Config, in io.Reader, out io.Writer) error {

	var formatters []StructuredLogFormatter

	if config.LogFormatterType == "" {
		formatters = GetAllFormatters()
	} else {
		formatter, ok := getFormatter(config.LogFormatterType)
		if !ok {
			return errors.Errorf("no formatter for '%s' found", config.LogFormatterType)
		}
		formatters = []StructuredLogFormatter{formatter}
	}

	type lineResult struct {
		lineNumber uint64
		original   string
		result     string
		err        error
	}
	resultsChan := make(chan lineResult)

	doneChan := make(chan bool, 1)

	go func() {
		receivedLineResults := map[uint64]lineResult{}
		currentLineNumber := uint64(0)
		foundResults := false

		for lr := range resultsChan {
			receivedLineResults[lr.lineNumber] = lr

			for {
				foundLineResult, ok := receivedLineResults[currentLineNumber]
				if !ok {
					break
				}
				if foundLineResult.result != "" {
					fmt.Fprintln(out, foundLineResult.result)
					foundResults = true
				} else {
					err := foundLineResult.err
					if err != nil {
						if config.Verbose || (!isSoftError(err) && config.LogFormatterType != "") {
							fmt.Fprintf(out, "Error on line %d: %s: %s\n", foundLineResult.lineNumber, err, foundLineResult.original)
						}
					}
				}
				currentLineNumber++
			}
		}

		if !foundResults {
			fmt.Fprintln(out, "no result found")
		}

		doneChan <- true

	}()

	reader := bufio.NewReader(in)

	// TODO(vishen): Allow configuration to be able to use a max number
	// of goroutines
	wg := sync.WaitGroup{}

	for i := uint64(0); ; i++ {
		// TODO(vishen): This is super inefficient...
		text, err := reader.ReadBytes('\n')
		if err != nil {
			break
		}

		wg.Add(1)
		go func(lineNumber uint64, line []byte) {
			defer wg.Done()
			lr := lineResult{
				original:   string(line),
				lineNumber: lineNumber,
			}
			for _, formatter := range formatters {
				result, err := SearchLine(config, line, formatter)
				if err == nil {
					lr.result = result
					break
				}
				lr.err = err
			}
			resultsChan <- lr
		}(i, text[:len(text)-1])

	}

	wg.Wait()
	close(resultsChan)

	<-doneChan

	return nil
}

func SearchLine(config Config, line []byte, formatter StructuredLogFormatter) (string, error) {
	valuesToPrint := make([]KV, 0, len(config.PrintKeys))

	found := false
	for _, v := range config.MatchOn {
		foundValue, err := formatter.GetValueFromLine(config, line, v.Key)
		if err != nil {
			return "", err
		}

		matched := false
		if v.Value != "" {
			matched = foundValue == v.Value
		} else if v.RegexString != "" {
			matched, _ = regexp.MatchString(v.RegexString, foundValue)
		}

		if !matched && config.MatchType == StructuredLogMatchTypeAnd {
			return "", ErrNoMatchingKeyValues
		}

		if matched {
			found = matched
		}

	}

	if !found && len(config.MatchOn) > 0 {
		return "", ErrNoMatchingKeyValues
	}

	for _, pk := range config.PrintKeys {
		pkv, err := formatter.GetValueFromLine(config, line, pk)
		if err != nil {
			return "", err
		}
		if pkv == "" {
			continue
		}
		valuesToPrint = append(valuesToPrint, KV{Key: pk, Value: fmt.Sprintf("%s", pkv)})
	}

	// TODO(vishen): It is possible to have config.printKeys that don't match
	// any line, this should NOT print the entire line? Currently it kind of
	// seems alright to default to printing the line if no matching valuesToPrint
	// are found.
	if len(valuesToPrint) == 0 {
		if len(config.PrintKeys) == 0 {
			return string(line), nil
		}
		return "", ErrNoMatchingPrintValues
	} else {
		return formatter.FormatFoundValues(config, valuesToPrint), nil
	}
}