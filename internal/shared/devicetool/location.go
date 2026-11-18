package devicetool

import (
	"math"
	"strconv"
	"strings"
)

// Setting the phone's location (issue #851): the node's phone is an
// emulator, whose GPS is set with its console's `geo fix`, as Appium sets an
// emulator's (appium/io.appium.settings at bf8c11b, lib/commands/geolocation.ts:26-75,
// Apache-2.0, notice in third_party/appium-settings). Maestro sets a
// location through an app of its own on the device instead
// (maestro-client AndroidDriver.kt:649-672), which needs that app installed.

// The bounds of a latitude and a longitude, in degrees.
const (
	maxLatitude  = 90
	maxLongitude = 180
)

func locationTool() Tool {
	return tool(Location, "Set the phone's GPS location, as if it stood there, so apps that look nearby or route "+
		"from here use it. Degrees, as a map gives them (21.02851, 105.85422 is Hanoi); altitude in meters.",
		`{"latitude":{"type":"number","minimum":-90,"maximum":90},`+
			`"longitude":{"type":"number","minimum":-180,"maximum":180},`+
			`"altitude":{"type":"number"}}`,
		[]string{"latitude", "longitude"})
}

// readLocation checks a location and keeps its values as `geo fix` is given them.
func (c *Call) readLocation(latitude, longitude, altitude *float64) error {
	if latitude == nil || longitude == nil {
		return bad("device_location needs latitude and longitude, in degrees")
	}

	if *latitude < -maxLatitude || *latitude > maxLatitude {
		return bad("latitude is from -%d to %d", maxLatitude, maxLatitude)
	}

	if *longitude < -maxLongitude || *longitude > maxLongitude {
		return bad("longitude is from -%d to %d", maxLongitude, maxLongitude)
	}

	c.Latitude, c.Longitude = geoValue(*latitude), geoValue(*longitude)
	if altitude != nil {
		c.Altitude = geoValue(*altitude)
	}

	return nil
}

// geoValue is a value rounded up to 5 decimals, as Appium rounds it
// ("Native ceil to 5 decimals": Math.ceil(v * 1e5) / 1e5), written as
// JavaScript writes the number: no trailing zeros, and 0 for -0.
func geoValue(v float64) string {
	rounded := math.Ceil(v*1e5) / 1e5
	if rounded == 0 {
		rounded = 0 // -0, as -0.000001 rounds up, is written 0.
	}

	return strconv.FormatFloat(rounded, 'f', -1, 64)
}

// GeoFix is the emulator console's `geo fix` arguments for a call: longitude,
// latitude and, when given, altitude. The node sends them, then the same with
// "," for "." (GeoFixComma).
func GeoFix(c Call) []string {
	args := []string{c.Longitude, c.Latitude}
	if c.Altitude != "" {
		args = append(args, c.Altitude)
	}

	return args
}

// GeoFixComma is GeoFix with "," for ".", which Appium sends after it as a
// workaround for the emulator's bug https://code.google.com/p/android/issues/detail?id=206180
// (JavaScript's replace, which replaces the first ".").
func GeoFixComma(c Call) []string {
	args := GeoFix(c)
	for i, a := range args {
		args[i] = strings.Replace(a, ".", ",", 1)
	}

	return args
}
