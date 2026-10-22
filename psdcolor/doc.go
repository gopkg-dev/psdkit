// Package psdcolor converts original PSD channel samples to owned, straight-alpha sRGB pixels.
// Conversion never changes the source planes. ICC matrix/TRC and monochrome profiles are supported;
// LUT profiles and unmanaged CMYK require an explicit error or caller-selected fallback.
package psdcolor
