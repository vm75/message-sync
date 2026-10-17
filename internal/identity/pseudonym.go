package identity

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
)

var adjectives = [...]string{
	"Amber", "Azure", "Brave", "Bright", "Calm", "Cedar", "Clever", "Coral",
	"Cosmic", "Crisp", "Dawn", "Echo", "Emerald", "Fair", "Fierce", "Fleet",
	"Frost", "Gentle", "Golden", "Grand", "Green", "Haven", "Indigo", "Jade",
	"Keen", "Lively", "Lunar", "Maple", "Merry", "Mist", "Moon", "Moss",
	"Mystic", "Nimble", "Noble", "Nova", "Opal", "Pine", "Polar", "Proud",
	"Quiet", "Radiant", "Rapid", "River", "Ruby", "Rust", "Sage", "Shadow",
	"Shining", "Silent", "Silver", "Sky", "Solar", "Spark", "Star", "Stellar",
	"Storm", "Swift", "Tidal", "Topaz", "True", "Velvet", "Vivid", "Wild",
}

var nouns = [...]string{
	"Badger", "Bear", "Beaver", "Bison", "Cedar", "Cheetah", "Crane", "Deer",
	"Dolphin", "Eagle", "Falcon", "Finch", "Fox", "Gecko", "Harbor", "Hare",
	"Hawk", "Heron", "Jaguar", "Lark", "Leopard", "Lion", "Llama", "Lynx",
	"Mantis", "Marten", "Meadow", "Mink", "Moose", "Ocean", "Osprey", "Otter",
	"Owl", "Panda", "Panther", "Parrot", "Pelican", "Penguin", "Plover", "Puma",
	"Quail", "Raven", "Robin", "Sailor", "Salmon", "Seal", "Shadow", "Sparrow",
	"Starling", "Stork", "Swan", "Swift", "Tiger", "Toucan", "Trout", "Turtle",
	"Viper", "Voyager", "Walrus", "Whale", "Willow", "Wolf", "Wombat", "Wren",
}

const suffixAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

// Pseudonym derives a deterministic, privacy-safe, human-friendly alias from an
// opaque actor identifier. No real-name or phone state is required or exposed.
func Pseudonym(opaqueID string) string {
	name, discriminator := pseudonymParts(opaqueID)
	if discriminator == "" {
		return name
	}
	return name + " " + discriminator
}

// PseudonymName returns the word-based alias without its discriminator.
func PseudonymName(opaqueID string) string {
	name, _ := pseudonymParts(opaqueID)
	return name
}

// Discriminator is stable for an actor regardless of anonymization or display
// settings. It is a short address component, not a globally unique identity.
func Discriminator(opaqueID string) string {
	_, discriminator := pseudonymParts(opaqueID)
	return discriminator
}

func pseudonymParts(opaqueID string) (string, string) {
	opaqueID = strings.TrimSpace(opaqueID)
	if opaqueID == "" {
		return "Member", ""
	}

	digest := sha256.Sum256([]byte("discord-pseudonym\x00" + opaqueID))

	adjIdx := int(binary.BigEndian.Uint16(digest[0:2])) % len(adjectives)
	nounIdx := int(binary.BigEndian.Uint16(digest[2:4])) % len(nouns)

	c1 := suffixAlphabet[digest[4]&31]
	c2 := suffixAlphabet[digest[5]&31]
	c3 := suffixAlphabet[digest[6]&31]
	c4 := suffixAlphabet[digest[7]&31]
	c5 := suffixAlphabet[digest[8]&31]

	return adjectives[adjIdx] + " " + nouns[nounIdx], fmt.Sprintf("%c%c%c%c%c", c1, c2, c3, c4, c5)
}

// DiscriminatorForLength preserves the historical five-character prefix.
// Valid configured lengths are 5 through 10; invalid lengths use five.
func DiscriminatorForLength(opaqueID string, length int) string {
	opaqueID = strings.TrimSpace(opaqueID)
	if opaqueID == "" {
		return ""
	}
	if length < 5 || length > 10 {
		length = 5
	}
	digest := sha256.Sum256([]byte("discord-pseudonym\x00" + opaqueID))
	code := make([]byte, length)
	for i := range code {
		code[i] = suffixAlphabet[digest[4+i]&31]
	}
	return string(code)
}

func PseudonymForLength(opaqueID string, length int) string {
	name := PseudonymName(opaqueID)
	code := DiscriminatorForLength(opaqueID, length)
	if code == "" {
		return name
	}
	return name + " " + code
}
