class TowersOfHanoi {
  constructor(root, discs = 6) {
    this.root = root;
    this.discs = discs;
    this.startScreen = root.querySelector("#start-screen");
    this.gameScreen = root.querySelector("#game-screen");
    this.startButton = root.querySelector("#start-button");
    this.towerEls = [...root.querySelectorAll(".tower")];
    this.statusEl = root.querySelector("#status");
    this.completionEl = root.querySelector("#completion");
    this.codeInput = root.querySelector("#attempt-code");
    this.copyButton = root.querySelector("#copy-code");
    this.copyStatus = root.querySelector("#copy-status");
    this.saveStatus = root.querySelector("#save-status");
    this.retryButton = root.querySelector("#retry-save");
    this.moves = [];
    this.savedMoves = 0;
    this.savedStart = false;
    this.saving = false;
    this.startedAt = null;
    this.completed = false;

    if (!globalThis.crypto?.getRandomValues) {
      this.saveStatus.textContent = "Open this task over HTTPS to start.";
      return;
    }
    this.code = "TH-" + TowersOfHanoi.randomHex();
    this.writeKey = TowersOfHanoi.randomHex();
    this.codeInput.value = this.code;
    this.startButton.disabled = false;
    this.copyButton.disabled = false;

    this.startButton.addEventListener("click", () => this.start());
    this.copyButton.addEventListener("click", () => this.copyCode());
    this.retryButton.addEventListener("click", () => this.save());
    window.addEventListener("online", () => this.save());
    window.addEventListener("pagehide", () => {
      if (this.startedAt !== null && (!this.savedStart || this.savedMoves < this.moves.length)) {
        // An in-flight save can overlap this retry. The server accepts identical events once.
        this.postProgress(this.savedMoves).catch(() => {});
      }
    });
    this.towerEls.forEach((towerEl) => {
      towerEl.addEventListener("click", () => this.selectTower(Number(towerEl.dataset.tower)));
    });
  }

  start() {
    this.startedAt = performance.now();
    this.towers = [[], [], []];
    for (let disc = this.discs; disc > 0; disc -= 1) this.towers[0].push(disc);

    this.selectedTower = null;
    this.startScreen.hidden = true;
    this.gameScreen.hidden = false;
    this.completionEl.hidden = true;
    this.statusEl.textContent = "Select a tower to move its top disc.";
    this.towerEls.forEach((towerEl) => { towerEl.disabled = false; });
    this.draw();
    this.towerEls[0].focus();
    this.save();
  }

  selectTower(towerIndex) {
    if (this.completed) return;

    if (this.selectedTower === null) {
      if (this.isEmpty(towerIndex)) return;
      this.selectedTower = towerIndex;
      this.statusEl.textContent = "Select a destination tower.";
      this.draw();
      return;
    }

    const fromTower = this.selectedTower;
    this.selectedTower = null;

    if (fromTower === towerIndex) {
      this.statusEl.textContent = "Move cancelled.";
      this.draw();
      return;
    }

    const elapsedMs = Math.round(performance.now() - this.startedAt);
    const valid = this.isMoveValid(fromTower, towerIndex);
    if (valid) this.moveDisc(fromTower, towerIndex);
    this.moves.push({
      from: fromTower + 1,
      to: towerIndex + 1,
      elapsedMs
    });
    this.statusEl.textContent = valid ? "Select a tower to move its top disc." : "That move is not allowed.";
    this.draw();

    if (this.isSolved()) this.complete();
    this.save();
  }

  isEmpty(towerIndex) {
    return this.towers[towerIndex].length === 0;
  }

  isMoveValid(fromTower, toTower) {
    return TowersOfHanoi.canMove(this.towers, fromTower, toTower);
  }

  static canMove(towers, fromTower, toTower) {
    const movingDisc = towers[fromTower][towers[fromTower].length - 1];
    const destinationTower = towers[toTower];
    const destinationDisc = destinationTower[destinationTower.length - 1];
    return movingDisc !== undefined && (destinationDisc === undefined || movingDisc < destinationDisc);
  }

  moveDisc(fromTower, toTower) {
    this.towers[toTower].push(this.towers[fromTower].pop());
  }

  isSolved() {
    return this.towers[0].length === 0 && this.towers[1].length === 0 && this.towers[2].length === this.discs;
  }

  draw() {
    const towerNames = ["Left", "Middle", "Right"];
    this.towerEls.forEach((towerEl, towerIndex) => {
      towerEl.replaceChildren();
      towerEl.classList.toggle("selected", towerIndex === this.selectedTower);

      this.towers[towerIndex].forEach((disc) => {
        const discEl = document.createElement("span");
        discEl.className = "disc";
        discEl.dataset.disc = disc;
        discEl.setAttribute("aria-hidden", "true");
        towerEl.append(discEl);
      });

      const discCount = this.towers[towerIndex].length;
      const selection = towerIndex === this.selectedTower ? ". Selected as the source." : ".";
      towerEl.setAttribute("aria-label", `${towerNames[towerIndex]} tower, ${discCount} ${discCount === 1 ? "disc" : "discs"}${selection}`);
    });
  }

  complete() {
    this.completed = true;
    this.statusEl.textContent = "";
    this.completionEl.hidden = false;
    this.towerEls.forEach((towerEl) => { towerEl.disabled = true; });

    this.copyButton.focus();
  }

  static randomHex() {
    return Array.from(crypto.getRandomValues(new Uint8Array(16)), (byte) => byte.toString(16).padStart(2, "0")).join("");
  }

  async copyCode() {
    try {
      await navigator.clipboard.writeText(this.code);
      this.copyStatus.textContent = "Code copied. Paste it into the survey.";
    } catch {
      this.codeInput.focus();
      this.codeInput.select();
      this.codeInput.setSelectionRange(0, this.code.length);
      this.copyStatus.textContent = "Code selected. Use your device’s Copy command, then paste it into the survey.";
    }
  }

  postProgress(offset, end = offset + 256) {
    return fetch("/api/towers/attempts", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ code: this.code, writeKey: this.writeKey, offset, moves: this.moves.slice(offset, end) }),
      keepalive: true
    });
  }

  async save() {
    if (this.startedAt === null || this.saving || (this.savedStart && this.savedMoves === this.moves.length)) return;
    this.saving = true;
    this.retryButton.hidden = true;
    this.saveStatus.textContent = "Saving attempt…";
    try {
      while (!this.savedStart || this.savedMoves < this.moves.length) {
        const end = Math.min(this.savedMoves + 256, this.moves.length);
        const response = await this.postProgress(this.savedMoves, end);
        if (!response.ok) throw new Error("Save failed");
        this.savedStart = true;
        this.savedMoves = end;
      }
      this.saveStatus.textContent = this.completed ? "Task saved. Paste your code into the survey." : "Attempt saved.";
    } catch {
      this.saveStatus.textContent = "Could not save the latest progress. Retry before leaving this page.";
      this.retryButton.hidden = false;
    } finally {
      this.saving = false;
    }
  }

  static runSelfCheck() {
    const legalTowers = [[6, 5, 4, 3, 2, 1], [], []];
    const invalidTowers = [[6, 5], [1], []];
    const check = (condition, message) => {
      if (!condition) throw new Error(message);
    };

    check(TowersOfHanoi.canMove(legalTowers, 0, 1), "A disc can move to an empty tower.");
    check(!TowersOfHanoi.canMove(invalidTowers, 0, 1), "A larger disc cannot move onto a smaller disc.");
    check(!TowersOfHanoi.canMove([[], [], []], 0, 1), "An empty tower cannot supply a disc.");
    return true;
  }
}

new TowersOfHanoi(document);

if (new URLSearchParams(window.location.search).has("self-check")) TowersOfHanoi.runSelfCheck();
