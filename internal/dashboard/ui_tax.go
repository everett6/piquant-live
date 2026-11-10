package dashboard

// taxPageHTML is the standalone, zero-dependency HTML5 control panel for crypto taxes.
const taxPageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="theme-color" content="#090d14">
<title>PiQuant Crypto Tax &amp; IRS Form 8949 Accounting</title>
<link rel="icon" href="/icon.svg" type="image/svg+xml">
<style>
  :root {
    --bg: #070a0f;
    --panel: #0d131c;
    --panel2: #121a26;
    --line: #1c2838;
    --line-bright: #27394f;
    --text: #e6edf3;
    --muted: #8ba2be;
    --accent: #00e5a3;
    --accent-dim: rgba(0, 229, 163, 0.12);
    --gold: #f59e0b;
    --gold-dim: rgba(245, 158, 11, 0.12);
    --bad: #ff5252;
    --bad-dim: rgba(255, 82, 82, 0.12);
  }
  * { box-sizing: border-box; }
  body {
    margin: 0;
    background: var(--bg);
    color: var(--text);
    font: 14px/1.5 ui-sans-serif, system-ui, -apple-system, sans-serif;
    padding-bottom: 60px;
  }
  header {
    background: rgba(13, 19, 28, 0.95);
    border-bottom: 1px solid var(--line);
    padding: 16px 28px;
    display: flex;
    justify-content: space-between;
    align-items: center;
    position: sticky;
    top: 0;
    z-index: 100;
    backdrop-filter: blur(8px);
  }
  .brand { display: flex; align-items: center; gap: 12px; }
  .dot { width: 12px; height: 12px; border-radius: 50%; background: var(--accent); box-shadow: 0 0 12px var(--accent); }
  h1 { margin: 0; font-size: 18px; font-weight: 700; letter-spacing: -0.3px; }
  .nav-btn {
    padding: 8px 14px;
    border-radius: 8px;
    border: 1px solid var(--line);
    background: var(--panel2);
    color: var(--text);
    font-size: 13px;
    font-weight: 600;
    text-decoration: none;
    cursor: pointer;
    transition: all 0.15s;
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
  .nav-btn:hover { background: var(--line-bright); color: #fff; }
  .nav-btn.primary { background: var(--accent); color: #051a11; border-color: var(--accent); }
  .nav-btn.primary:hover { background: #1bf0ae; }
  .container { max-width: 1300px; margin: 24px auto; padding: 0 20px; }
  .grid { display: grid; gap: 16px; margin-bottom: 24px; }
  .grid-5 { grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); }
  .card {
    background: var(--panel);
    border: 1px solid var(--line);
    border-radius: 14px;
    padding: 20px;
    box-shadow: 0 8px 24px rgba(0,0,0,0.3);
  }
  .card.highlight {
    border-color: rgba(245, 158, 11, 0.4);
    background: linear-gradient(180deg, var(--gold-dim) 0%, var(--panel) 100%);
  }
  .k { font-size: 11px; text-transform: uppercase; letter-spacing: 0.08em; color: var(--muted); font-weight: 600; margin-bottom: 6px; }
  .v { font-size: 26px; font-weight: 750; font-variant-numeric: tabular-nums; }
  .s { font-size: 12px; color: var(--muted); margin-top: 4px; }
  table { width: 100%; border-collapse: collapse; font-size: 13px; text-align: left; }
  th { padding: 12px 14px; font-size: 11px; text-transform: uppercase; letter-spacing: 0.06em; color: var(--muted); border-bottom: 1px solid var(--line); background: var(--panel2); }
  td { padding: 12px 14px; border-bottom: 1px solid rgba(255,255,255,0.04); font-variant-numeric: tabular-nums; }
  tr:hover td { background: rgba(255,255,255,0.02); }
  .mono { font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace; }
  .pill { display: inline-block; padding: 3px 8px; border-radius: 6px; font-size: 11px; font-weight: 600; }
  .pill.ok { background: var(--accent-dim); color: var(--accent); }
  .pill.gold { background: var(--gold-dim); color: var(--gold); }
  .pill.bad { background: var(--bad-dim); color: var(--bad); }
  .pill.muted { background: rgba(255,255,255,0.06); color: var(--muted); }
  .table-container { overflow-x: auto; border-radius: 10px; border: 1px solid var(--line); margin-top: 12px; }
  .flex-between { display: flex; justify-content: space-between; align-items: center; }
</style>
</head>
<body>

<header>
  <div class="brand">
    <div class="dot"></div>
    <div>
      <h1>PiQuant Crypto Tax Accounting &amp; IRS Form 8949</h1>
      <div style="font-size:12px;color:var(--muted)">Automated FIFO Capital Gains, Deductible Fees &amp; Safe Harbor Quarterly Escrow</div>
    </div>
  </div>
  <div style="display:flex;gap:10px;align-items:center">
    <select id="taxYearSelect" class="nav-btn" onchange="loadTaxData()">
      <option value="2026">Tax Year 2026</option>
      <option value="2025">Tax Year 2025</option>
      <option value="0">All Historical Years</option>
    </select>
    <a href="/api/tax/export.csv" id="btnExportCSV" class="nav-btn primary" download>📥 Download Form 8949 CSV</a>
    <a href="/" class="nav-btn">← Back to Dashboard</a>
  </div>
</header>

<div class="container">
  <!-- Key Tax Metrics -->
  <div class="grid grid-5">
    <div class="card highlight">
      <div class="k" style="color:var(--gold)">★ Total Tax Reserve To Set Aside</div>
      <div class="v" id="cardTotalReserve" style="color:var(--gold)">$0.00</div>
      <div class="s" id="cardReserveSub">Federal 24.0% + California 9.3%</div>
    </div>
    <div class="card">
      <div class="k">Net Taxable Capital Gains</div>
      <div class="v" id="cardNetGain" style="color:var(--accent)">$0.00</div>
      <div class="s" id="cardTradesProcessed">0 Dispositions Processed</div>
    </div>
    <div class="card">
      <div class="k">Estimated Federal Tax (IRS)</div>
      <div class="v" id="cardFedTax">$0.00</div>
      <div class="s">IRS Short-Term Ordinary Bracket</div>
    </div>
    <div class="card">
      <div class="k">Estimated State Tax (CA FTB)</div>
      <div class="v" id="cardStateTax">$0.00</div>
      <div class="s">California Franchise Tax Board</div>
    </div>
    <div class="card">
      <div class="k">Deductible Trading Fees</div>
      <div class="v" id="cardFeesDeducted" style="color:var(--muted)">$0.00</div>
      <div class="s">Subtracted from Taxable Gross</div>
    </div>
  </div>

  <!-- Safe Harbor Quarterly Escrow Schedule -->
  <div class="card" style="margin-bottom:24px">
    <div class="flex-between">
      <div>
        <h2 style="margin:0;font-size:16px;font-weight:700">Safe Harbor Quarterly Estimated Tax Schedule (IRS 1040-ES &amp; CA 540-ES)</h2>
        <div class="s">Avoid IRS underpayment penalties by setting aside and remitting quarterly payments on realized trading profits</div>
      </div>
      <span class="pill gold">State of California + IRS Schedule</span>
    </div>
    <div class="table-container">
      <table>
        <thead>
          <tr>
            <th>Quarter</th>
            <th>Period Covered</th>
            <th>IRS &amp; CA FTB Due Date</th>
            <th style="text-align:right">Realized Gain</th>
            <th style="text-align:right">Federal Tax Due</th>
            <th style="text-align:right">CA State Tax Due</th>
            <th style="text-align:right">Total Reserve Due</th>
            <th style="text-align:center">Milestone Status</th>
          </tr>
        </thead>
        <tbody id="quarterlyScheduleBody" class="mono">
          <tr><td colspan="8" style="text-align:center;padding:16px;color:var(--muted)">Loading tax quarters...</td></tr>
        </tbody>
      </table>
    </div>
  </div>

  <!-- Form 8949 Lot Dispositions -->
  <div class="card">
    <div class="flex-between">
      <div>
        <h2 style="margin:0;font-size:16px;font-weight:700">IRS Form 8949 — Sales and Other Dispositions of Capital Assets</h2>
        <div class="s">Strict FIFO cost-basis lot matching with deductible maker/taker exchange fee adjustments</div>
      </div>
      <div style="font-size:12px;color:var(--muted)" id="dispositionCount">0 Dispositions Recorded</div>
    </div>
    <div class="table-container">
      <table>
        <thead>
          <tr>
            <th>Property Description</th>
            <th>Date Acquired</th>
            <th>Date Sold</th>
            <th style="text-align:right">Gross Proceeds</th>
            <th style="text-align:right">Cost Basis</th>
            <th style="text-align:right">Fee Adjustment</th>
            <th style="text-align:right">Gain or (Loss)</th>
            <th>Holding Term</th>
            <th style="text-align:center">Quarter</th>
          </tr>
        </thead>
        <tbody id="dispositionsTableBody" class="mono">
          <tr><td colspan="9" style="text-align:center;padding:24px;color:var(--muted)">Analyzing trade records from journal...</td></tr>
        </tbody>
      </table>
    </div>
  </div>
</div>

<script>
  function formatMoney(v) {
    if (v === null || v === undefined || isNaN(v)) return "$0.00";
    var parts = Number(v).toFixed(2).split(".");
    parts[0] = parts[0].replace(/\B(?=(\d{3})+(?!\d))/g, ",");
    return (v < 0 ? "-$" : "$") + parts.join(".").replace("-", "");
  }

  function loadTaxData() {
    var year = document.getElementById("taxYearSelect").value;
    document.getElementById("btnExportCSV").href = "/api/tax/export.csv?year=" + year;
    fetch("/api/tax?year=" + year)
      .then(function(r) { return r.json(); })
      .then(function(data) {
        document.getElementById("cardTotalReserve").textContent = formatMoney(data.total_tax_reserve_usd);
        document.getElementById("cardReserveSub").textContent = "Federal " + (data.federal_rate_pct||24).toFixed(1) + "% + California " + (data.state_rate_pct||9.3).toFixed(1) + "%";
        
        var netGain = data.net_taxable_gain_usd || 0;
        var elNet = document.getElementById("cardNetGain");
        elNet.textContent = formatMoney(netGain);
        elNet.style.color = netGain >= 0 ? "var(--accent)" : "var(--bad)";

        document.getElementById("cardTradesProcessed").textContent = (data.total_dispositions || 0) + " Dispositions Processed";
        document.getElementById("cardFedTax").textContent = formatMoney(data.estimated_federal_tax);
        document.getElementById("cardStateTax").textContent = formatMoney(data.estimated_state_tax);
        document.getElementById("cardFeesDeducted").textContent = formatMoney(data.total_fees_usd);

        // Render Quarterly Schedule
        var qBody = document.getElementById("quarterlyScheduleBody");
        if (data.quarterly_schedule && data.quarterly_schedule.length) {
          qBody.innerHTML = data.quarterly_schedule.map(function(q) {
            var statusPill = "<span class='pill muted'>UPCOMING</span>";
            if (q.is_past_due) statusPill = "<span class='pill bad'>PAST DUE</span>";
            else if (q.is_current) statusPill = "<span class='pill ok'>CURRENT QUARTER</span>";

            return "<tr>" +
              "<td><strong>" + q.quarter + "</strong></td>" +
              "<td>" + q.period + "</td>" +
              "<td>" + q.due_date + "</td>" +
              "<td style='text-align:right'>" + formatMoney(q.realized_gain) + "</td>" +
              "<td style='text-align:right'>" + formatMoney(q.federal_tax) + "</td>" +
              "<td style='text-align:right'>" + formatMoney(q.state_tax) + "</td>" +
              "<td style='text-align:right;color:var(--gold);font-weight:700'>" + formatMoney(q.total_reserve) + "</td>" +
              "<td style='text-align:center'>" + statusPill + "</td>" +
            "</tr>";
          }).join("");
        }

        // Render Dispositions Table
        var dBody = document.getElementById("dispositionsTableBody");
        document.getElementById("dispositionCount").textContent = (data.dispositions ? data.dispositions.length : 0) + " Dispositions Recorded";
        if (data.dispositions && data.dispositions.length) {
          dBody.innerHTML = data.dispositions.map(function(d) {
            var gColor = d.net_gain_usd >= 0 ? "var(--accent)" : "var(--bad)";
            return "<tr>" +
              "<td>" + d.description + "</td>" +
              "<td>" + d.acquired_at.split("T")[0] + "</td>" +
              "<td>" + d.sold_at.split("T")[0] + "</td>" +
              "<td style='text-align:right'>" + formatMoney(d.proceeds_usd) + "</td>" +
              "<td style='text-align:right'>" + formatMoney(d.cost_basis_usd) + "</td>" +
              "<td style='text-align:right;color:var(--muted)'>" + formatMoney(d.fee_usd) + "</td>" +
              "<td style='text-align:right;color:" + gColor + ";font-weight:700'>" + formatMoney(d.net_gain_usd) + "</td>" +
              "<td><span class='pill " + (d.holding_type === "SHORT_TERM" ? "ok" : "gold") + "'>" + d.holding_type + "</span></td>" +
              "<td style='text-align:center'>" + d.quarter + "</td>" +
            "</tr>";
          }).join("");
        } else {
          dBody.innerHTML = "<tr><td colspan='9' style='text-align:center;padding:24px;color:var(--muted)'>No lot sales recorded yet for this year.</td></tr>";
        }
      })
      .catch(function(err) {
        console.error("Failed to load tax data:", err);
      });
  }

  // Load immediately on page render and refresh every 10 seconds
  loadTaxData();
  setInterval(loadTaxData, 10000);
</script>

</body>
</html>
`
