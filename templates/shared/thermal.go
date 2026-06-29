package shared

// ThermalFrom builds the ThermalData for a receipt view: it resolves the paper
// width from the saved setting (an explicit ?size= overrides it) and derives the
// matching size-switch link, so every receipt view sizes to the roll exactly like
// the sales receipt. receiptWidth is Settings.ReceiptWidth ("58"/"80"); base is
// the view's own URL (no query); printURL is the POST endpoint that re-sends the
// ESC/POS slip.
func ThermalFrom(receiptWidth, sizeParam, title, base, printURL string) ThermalData {
	narrow := receiptWidth == "58"
	if sizeParam != "" {
		narrow = sizeParam == "58"
	}
	switchURL, switchText := base+"?size=58", "Switch to 58mm"
	if narrow {
		switchURL, switchText = base+"?size=80", "Switch to 80mm"
	}
	return ThermalData{
		Title:      title,
		Narrow:     narrow,
		PrintURL:   printURL,
		SwitchURL:  switchURL,
		SwitchText: switchText,
	}
}
