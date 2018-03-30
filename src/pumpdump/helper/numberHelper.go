package helper

import "strconv"

// Float64ToString ...
func Float64ToString(input float64) string {
	// to convert a float number to a string
	return strconv.FormatFloat(input, 'f', 8, 64)
}

// StringToFloat64 ...
func StringToFloat64(input string) float64 {
	// to convert a float number to a string
	num, err := strconv.ParseFloat(input, 64)
	if err != nil {
		return 0
	}
	return num
}
