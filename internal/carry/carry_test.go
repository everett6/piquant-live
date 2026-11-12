package carry

import (
	"archive/zip"
	"bytes"
	"math"
	"strings"
	"testing"
)

func near(t *testing.T, what string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("%s = %.15f, want %.15f", what, got, want)
	}
}

func TestStepMatchesPREREG19(t *testing.T) {
	st := New("2026-11-11", map[string]float64{"BTC": 100, "ETH": 10})
	near(t, "open", st.Equity, 1-(3e-4+7e-4)/1.5)
	open := st.Equity
	flat := map[string]DayInput{"BTC": {Close: 100, High: 100, FundPos: 3e-4}, "ETH": {Close: 10, High: 10, FundPos: 3e-4}}
	if err := st.Step("2026-11-12", flat); err != nil {
		t.Fatal(err)
	}
	near(t, "funding day", st.Equity, open*(1+3e-4/1.5))
	near(t, "stress funding day", st.Days[0].RetStress, 0.75*3e-4/1.5)
	if err := st.Step("2026-11-14", flat); err == nil {
		t.Fatal("skipped day accepted")
	}
	// BTC +10% close with a liquidation high; ETH negative funding
	in := map[string]DayInput{"BTC": {Close: 110, High: 151}, "ETH": {Close: 10, High: 10, FundNeg: -3e-4}}
	if err := st.Step("2026-11-13", in); err != nil {
		t.Fatal(err)
	}
	btc := -(0.10*(3e-4+7e-4) + 0.01 + 7e-4) / 1.5
	eth := -3e-4 / 1.5
	near(t, "liq day", st.Days[1].Ret, (btc+eth)/2)
	if st.Liquidations["BTC"] != 1 || !st.Days[1].Liq || st.MaxDD <= 0 {
		t.Fatalf("state %+v", st)
	}
	if !strings.Contains(st.Summary(), "liquidations map[BTC:1]") {
		t.Fatal(st.Summary())
	}
}

func TestParseFunding(t *testing.T) {
	csvData := "calc_time,funding_interval_hours,last_funding_rate\n1794441600000,8,0.0001\n1794470400000,8,-0.00005\n"
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("BTCUSDT-fundingRate-2026-11.csv")
	_, _ = w.Write([]byte(csvData))
	_ = zw.Close()
	rates, err := ParseFundingZip(buf.Bytes())
	if err != nil || len(rates) != 2 {
		t.Fatalf("%v %v", rates, err)
	}
	d := DailyFunding(rates)
	v := d[rates[0].At.Format("2006-01-02")]
	near(t, "pos", v[0], 0.0001)
	near(t, "neg", v[1], -0.00005)
	noHeader, _ := ParseFundingCSV(strings.NewReader("1794441600000,8,0.0002\n"))
	if len(noHeader) != 1 || noHeader[0].Rate != 0.0002 {
		t.Fatal("headerless csv")
	}
}
