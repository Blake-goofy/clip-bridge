package main

import (
	"crypto/rand"
	"strings"
)

const sessionWordCount = 5

var sessionWords = [...]string{
	"able", "acid", "acre", "aged", "ally", "amber", "angel", "apple",
	"apron", "arrow", "atlas", "aunt", "badge", "baker", "basic", "beach",
	"bead", "beam", "bean", "bear", "bench", "bird", "blend", "blink",
	"block", "bloom", "board", "boat", "bonus", "book", "boost", "boot",
	"brave", "bread", "brick", "bridge", "bright", "brush", "buddy", "bunch",
	"cabin", "cable", "cactus", "candy", "canoe", "canvas", "card", "cargo",
	"carpet", "cedar", "chair", "chalk", "charm", "chase", "cheer", "cherry",
	"chill", "clay", "clean", "cliff", "clock", "cloud", "clover", "coach",
	"coast", "cocoa", "coin", "coral", "corn", "cotton", "craft", "crisp",
	"crown", "dance", "dawn", "deer", "desk", "dime", "dinner", "dirt",
	"dock", "donut", "door", "draft", "dream", "dress", "drift", "drum",
	"dust", "eagle", "earth", "easy", "echo", "ember", "entry", "equal",
	"fair", "fall", "farm", "feast", "fence", "field", "film", "fire",
	"fish", "flag", "flame", "flash", "flint", "floor", "flower", "foam",
	"focus", "forest", "fork", "fresh", "frost", "fruit", "garden", "gear",
	"giant", "ginger", "glass", "globe", "glow", "goat", "gold", "grain",
	"grape", "grass", "green", "grin", "grove", "guide", "happy", "harbor",
	"hazel", "heart", "hill", "honey", "honor", "horse", "hotel", "house",
	"idea", "ivory", "jacket", "jazz", "jewel", "jolly", "juice", "kite",
	"label", "lake", "lamp", "laser", "leaf", "lemon", "level", "light",
	"linen", "lion", "lunar", "magic", "maple", "marble", "march", "meadow",
	"melon", "metal", "mint", "mirror", "model", "money", "moon", "morning",
	"motor", "mount", "music", "navy", "nectar", "nest", "night", "north",
	"novel", "oasis", "ocean", "olive", "onion", "orbit", "orchid", "otter",
	"paint", "paper", "park", "peach", "pearl", "penny", "pepper", "piano",
	"pilot", "pine", "pixel", "plain", "planet", "plaza", "pond", "porch",
	"pride", "print", "prize", "puzzle", "quiet", "rabbit", "radar", "raven",
	"river", "robot", "rocket", "rose", "royal", "ruby", "sail", "salad",
	"salsa", "sand", "scale", "scene", "scout", "seed", "shadow", "shell",
	"shine", "ship", "silver", "simple", "sketch", "sky", "slate", "smile",
	"snow", "solar", "spark", "spice", "spoon", "spring", "square", "star",
	"stone", "story", "sugar", "sunny", "swift", "table", "tango", "tea",
}

func randomSessionID() (string, error) {
	b := make([]byte, sessionWordCount)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	parts := make([]string, len(b))
	for i, v := range b {
		parts[i] = sessionWords[int(v)]
	}
	return strings.Join(parts, "-"), nil
}
