# Lauren’s Tower of Hanoi request

This note describes the original standalone prototype. See the [ClipBridge study setup](../../README.md#tower-of-hanoi-study) for the hosted version.

## Goal

Adapt the existing Tower of Hanoi game for use as a participant task in a Qualtrics survey, then keep completion timing and move data available privately to the researcher.

## Requested participant experience

- Show a start screen with the instruction **“Click ‘Start’ to begin task”** and a **Start** button. Starting the task opens the game.
- Use six discs instead of the current five.
- Keep this instruction visible throughout play: **“Move all the discs from the left tower to the right one at a time. A disc cannot be on top of a smaller disc. Have fun!”**
- Remove the **Solve** and **Restart** options.
- Change the colors to:
  - Background: `#8babf1` (blue)
  - Pegs: `#c44601` (brown)
  - Text: `#000` (black)

## Research data requested

- Start the timer when the participant presses **Start** and record elapsed time when they finish.
- Keep a researcher-accessible record of successful moves, invalid moves, and total moves if possible. This information should not be shown to participants.
- Definitions supplied by Lauren:
  - A **successful move** transfers one disc to another peg legally, including onto an empty peg.
  - An **invalid move** is an attempt to place a larger disc on top of a smaller disc.
  - A move is one disc moved at a time.

The intended completion condition is to move every disc from the left peg to the right peg.

## Current local implementation

- `index.html` is a complete standalone page with the requested Start screen and no Solve or Restart controls.
- `game.js` initializes a six-disc game only after Start. It keeps the instructions visible, measures elapsed time from Start to completion, counts successful and invalid moves privately, and records each successful move with its timestamp. Completion data is available in the browser developer console as `window.towerOfHanoiResult`.
- `styles.css` uses Lauren’s blue background, brown pegs, and black text. It is plain browser CSS and supports six discs.

## Qualtrics integration

The game can likely run inside a Qualtrics question and save results as embedded data, provided Lauren’s account allows custom HTML and JavaScript. The local files need an adapter for Qualtrics’ question environment. See [Qualtrics integration feasibility](qualtrics-feasibility.md) for the recommended setup, constraints, and pilot check.

## Rules to present or preserve

1. Move only one disc at a time.
2. Never place a larger disc on a smaller disc.
3. Finish by moving all discs from the left peg to the right peg.

