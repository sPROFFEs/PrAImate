<script>
  import { onMount, onDestroy } from 'svelte'
  import { api } from '../lib/api.js'
  import { activePage } from '../lib/stores.js'
  import { createPoller } from '../lib/poller.js'

  const now = new Date()
  const currentMonth = now.toISOString().slice(0, 7)
  let month = currentMonth
  let data = null
  let error = ''
  let loading = true
  let disposed = false
  let series = 'tokens'
  const number = new Intl.NumberFormat('en', { maximumFractionDigits: 0 })
  const compact = new Intl.NumberFormat('en', { notation: 'compact', maximumFractionDigits: 1 })
  const fmt = (value) => number.format(value || 0)
  const short = (value) => compact.format(value || 0)
  const monthLabel = (value) => new Date(`${value}-01T12:00:00Z`).toLocaleDateString('en', { month: 'long', year: 'numeric', timeZone: 'UTC' })
  const cliLabel = (value) => ({ 'praimate-cli': 'PrAImate CLI', 'praimate-code': 'PrAImate Code', 'codex': 'Codex', 'claude': 'Claude Code', 'openclaude': 'OpenClaude', 'opencode': 'OpenCode', 'native': 'Native endpoint' })[value] || value
  const poller = createPoller(load, { delay: () => 30000 })

  async function load() {
    const requested = month
    loading = true
    try {
      const result = await api.usageDashboard(requested)
      if (!disposed && month === requested) { data = result; error = '' }
    } catch (e) {
      if (!disposed && month === requested) error = String(e)
    } finally {
      if (!disposed && month === requested) loading = false
    }
  }
  function changeMonth() {
    if (!/^\d{4}-\d{2}$/.test(month)) month = currentMonth
    data = null
    poller.request()
  }
  onMount(() => {
    poller.request()
    const visible = () => { if (!document.hidden) poller.request() }
    document.addEventListener('visibilitychange', visible)
    return () => document.removeEventListener('visibilitychange', visible)
  })
  onDestroy(() => { disposed = true; poller.stop() })

  $: totals = data?.totals || {}
  $: dayCount = month === currentMonth ? now.getUTCDate() : new Date(Date.UTC(Number(month.slice(0,4)), Number(month.slice(5,7)), 0)).getUTCDate()
  $: days = Array.from({ length: dayCount }, (_, index) => {
    const name = `${month}-${String(index + 1).padStart(2, '0')}`
    return { name, value: data?.days?.find((day) => day.name === name)?.[series] || 0 }
  })
  $: peak = Math.max(1, ...days.map((day) => day.value))
  $: points = days.map((day, index) => [12 + index * 736 / Math.max(1, days.length - 1), 158 - day.value / peak * 138])
  $: line = points.map(([x,y], index) => `${index ? 'L' : 'M'}${x},${y}`).join(' ')
  $: months = Array.from({ length: 6 }, (_, index) => {
    const date = new Date(`${month}-01T12:00:00Z`)
    date.setUTCMonth(date.getUTCMonth() - 5 + index)
    const name = date.toISOString().slice(0,7)
    return { name, label: date.toLocaleDateString('en', { month: 'short', timeZone: 'UTC' }), tokens: data?.months?.find((item) => item.name === name)?.tokens || 0 }
  })
  $: monthPeak = Math.max(1, ...months.map((item) => item.tokens))
</script>

<section class="dashboard" aria-label="Usage dashboard" aria-busy={loading}>
  <header class="dashboard-heading">
    <div><p class="eyebrow">YOUR WORKSPACE</p><h1>Overview</h1><p class="subtitle">Your models, conversations and activity in one place.</p></div>
    <div class="toolbar">
      <label class="month-picker"><span class="sr-only">Usage month</span><input aria-label="Usage month" type="month" bind:value={month} max={currentMonth} on:change={changeMonth} /></label>
      <button class="btn refresh" aria-label="Refresh usage" title="Refresh usage" disabled={loading} on:click={() => poller.request()}><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 7v5h-5M4 17v-5h5M6.1 7a7 7 0 0 1 11.5-2L20 8M4 16l2.4 3A7 7 0 0 0 18 17" /></svg></button>
    </div>
  </header>

  {#if error}<div class="banner" role="alert">{error}</div>{/if}

  <div class="overview-band">
    <div><span class="live-dot"></span><strong>{monthLabel(month)}</strong><span class="band-caption"> · your activity at a glance</span></div>
    <span class="privacy"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 10h12v11H6zM8 10V6a4 4 0 0 1 8 0v4M12 14v3" /></svg>Encrypted in your profile</span>
  </div>

  <div class="metrics">
    <article class="metric featured"><span>Reported tokens</span><strong title={fmt(totals.tokens)}>{loading && !data ? '—' : short(totals.tokens)}</strong><small><span class="input-dot"></span>{short(totals.inputTokens)} input <span class="output-dot"></span>{short(totals.outputTokens)} output</small></article>
    <article class="metric"><span>Runs</span><strong>{loading && !data ? '—' : fmt(totals.runs)}</strong><small>Chat turns, worker requests and terminal calls</small></article>
    <article class="metric"><span>Average tokens / run</span><strong>{totals.reportedRuns ? short(totals.averageTokensPerRun) : '—'}</strong><small>Across runs reporting usage</small></article>
    <article class="metric"><span>Active days</span><strong>{loading && !data ? '—' : fmt(totals.activeDays)}</strong><small>{short(totals.averageTokensPerDay)} reported tokens / calendar day</small></article>
  </div>

  {#if data && !totals.runs}
    <div class="empty-state"><div class="empty-mark" aria-hidden="true">↗</div><div><h2>Your activity will appear here</h2><p>Start a chat or a worker run. Usage is collected locally from this update onward.</p></div><button class="btn primary" on:click={() => activePage.set('chats')}>Open chats <span aria-hidden="true">→</span></button></div>
  {/if}

  <div class="charts">
    <article class="panel activity-panel">
      <header class="panel-heading"><div><h2>Daily activity</h2><p>Provider-reported tokens</p></div><div class="segmented" aria-label="Token series">{#each [['tokens','All'],['inputTokens','Input'],['outputTokens','Output']] as [value,label]}<button class:chosen={series === value} aria-pressed={series === value} on:click={() => series = value}>{label}</button>{/each}</div></header>
      <div class="plot">
        <span class="plot-max">{short(peak === 1 ? 0 : peak)}</span>
        <svg class="line-chart" viewBox="0 0 760 180" role="img" aria-label={`${monthLabel(month)} daily reported tokens`}>
          <defs><linearGradient id="usage-area" x1="0" x2="0" y1="0" y2="1"><stop offset="0%" stop-color="var(--accent)" stop-opacity=".24"/><stop offset="100%" stop-color="var(--accent)" stop-opacity=".015"/></linearGradient></defs>
          {#each [20,66,112,158] as y}<line x1="12" x2="748" y1={y} y2={y} class="gridline" />{/each}
          <path d={`${line} L${points.at(-1)?.[0] || 12},158 L12,158 Z`} fill="url(#usage-area)" />
          <path d={line} class="trend-line" />
          {#each points as [x,y], index}<circle cx={x} cy={y} r="4" class="plot-point" tabindex="0" aria-label={`${days[index].name}: ${fmt(days[index].value)} tokens`}><title>{days[index].name}: {fmt(days[index].value)} tokens</title></circle>{/each}
        </svg>
        <div class="chart-dates"><span>01</span><span>{String(Math.ceil(dayCount / 2)).padStart(2,'0')}</span><span>{String(dayCount).padStart(2,'0')}</span></div>
      </div>
    </article>
    <article class="panel monthly-panel">
      <header class="panel-heading"><div><h2>Monthly rhythm</h2><p>Six months of reported usage</p></div></header>
      <div class="month-bars">{#each months as item}<div class="month-column"><span class="bar-value">{short(item.tokens)}</span><div class="bar-track"><div class="bar" class:current={item.name === month} style={`height:${item.tokens ? Math.max(3, item.tokens / monthPeak * 100) : 2}%`} title={`${monthLabel(item.name)}: ${fmt(item.tokens)} tokens`}></div></div><span>{item.label}</span></div>{/each}</div>
    </article>
  </div>

  <div class="rankings">
    <article class="panel"><header class="panel-heading"><div><h2>Your go-to CLIs</h2><p>Ranked by runs this month</p></div><span class="count-pill">{data?.clis?.length || 0}</span></header>
      <div class="ranking-list">{#each (data?.clis || []).slice(0,6) as item, index}<div class="ranking-row"><span class="rank">{String(index + 1).padStart(2,'0')}</span><div class="ranking-info"><div class="ranking-title"><strong>{cliLabel(item.name)}</strong><span>{fmt(item.runs)} <small>{item.runs === 1 ? 'run' : 'runs'}</small></span></div><div class="meter"><span style={`width:${totals.runs ? item.runs / totals.runs * 100 : 0}%`}></span></div></div></div>{:else}<p class="no-ranking">No CLI activity recorded for this month.</p>{/each}</div>
    </article>
    <article class="panel"><header class="panel-heading"><div><h2>Models in rotation</h2><p>Selected or reported model per run</p></div><span class="count-pill">{data?.models?.length || 0}</span></header>
      <div class="ranking-list">{#each (data?.models || []).slice(0,6) as item, index}<div class="ranking-row"><span class="model-mark" aria-hidden="true">◇</span><div class="ranking-info"><div class="ranking-title"><strong title={item.name}>{item.name}</strong><span>{fmt(item.runs)} <small>{item.runs === 1 ? 'run' : 'runs'}</small></span></div><div class="meter muted"><span style={`width:${totals.runs ? item.runs / totals.runs * 100 : 0}%`}></span></div></div></div>{:else}<p class="no-ranking">Your selected models will appear as you work.</p>{/each}</div>
    </article>
  </div>
  <footer class="coverage"><span class="coverage-mark" aria-hidden="true">i</span><p><strong>Usage coverage.</strong> Token usage was reported for {fmt(totals.reportedRuns)} of {fmt(totals.runs)} runs. Missing reports are not estimated. Includes Chat, Studio, Workers and supported terminals launched by PrAImate. Terminal calls are recorded as separate runs. Antigravity terminal usage and external terminals are not measured; older CLI versions may omit reports. Dates use UTC.</p></footer>
</section>

<style>
  .dashboard { max-width:1400px; margin:0 auto; color:var(--text); padding-bottom:16px; }
  .dashboard-heading,.toolbar,.overview-band,.privacy,.panel-heading,.ranking-title { display:flex; align-items:center; justify-content:space-between; gap:12px; }
  .dashboard-heading { margin:4px 0 26px; }
  .eyebrow { color:var(--text-dim); font-size:10px; font-weight:650; letter-spacing:1.6px; margin:0 0 5px; }
  h1 { font-size:30px; font-weight:650; letter-spacing:-1px; margin:0; }
  .subtitle { margin:4px 0 0; color:var(--text-dim); }
  .toolbar { justify-content:flex-end; }
  .month-picker input { font:inherit; color:var(--text); color-scheme:inherit; background:var(--bg-panel); border:1px solid var(--border-bright); border-radius:var(--radius-sm); padding:8px 10px; }
  .refresh { padding:9px; }
  svg { fill:none; stroke:currentColor; stroke-width:1.7; stroke-linecap:round; stroke-linejoin:round; }
  .refresh svg { width:17px; height:17px; }
  .overview-band { color:var(--text-dim); margin-bottom:14px; font-size:12px; }
  .overview-band strong { color:var(--text); font-weight:550; }
  .live-dot { display:inline-block; height:6px; width:6px; border-radius:50%; background:var(--accent); margin-right:8px; }
  .privacy { font-size:11px; gap:6px; white-space:nowrap; }
  .privacy svg { width:13px; height:13px; }
  .metrics { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)); gap:12px; margin-bottom:20px; }
  .metric,.panel { background:var(--bg-panel); border:1px solid var(--border); border-radius:var(--radius); box-shadow:var(--shadow-sm); min-width:0; }
  .metric { padding:20px; display:flex; flex-direction:column; gap:9px; }
  .metric > span { color:var(--text-dim); font-size:12px; }
  .metric strong { font-size:34px; letter-spacing:-1.5px; font-weight:600; font-variant-numeric:tabular-nums; line-height:1.2; }
  .metric small { color:var(--text-dim); font-size:10.5px; line-height:1.5; }
  .featured { background:linear-gradient(135deg,var(--accent-soft),var(--bg-panel) 85%); border-color:color-mix(in srgb,var(--accent) 22%,var(--border)); }
  .input-dot,.output-dot { display:inline-block; width:5px; height:5px; border-radius:50%; background:var(--accent); margin:0 5px 1px 0; }
  .output-dot { background:var(--ok); margin-left:10px; }
  .charts { display:grid; grid-template-columns:minmax(0,1.8fr) minmax(0,1fr); gap:16px; margin-bottom:16px; }
  .panel { padding:20px; }
  .panel-heading { margin-bottom:18px; }
  h2 { margin:0; font-size:14px; font-weight:600; letter-spacing:-.2px; }
  .panel-heading p { margin:4px 0 0; color:var(--text-dim); font-size:11px; }
  .segmented { display:flex; padding:3px; background:var(--bg-raised); border-radius:8px; }
  .segmented button { background:transparent; color:var(--text-dim); border:0; padding:5px 9px; border-radius:5px; font:inherit; font-size:10px; cursor:pointer; }
  .segmented .chosen { color:var(--text); background:var(--bg-panel); box-shadow:var(--shadow-sm); }
  .plot { position:relative; padding-top:12px; }
  .plot-max { font-size:10px; color:var(--text-dim); }
  .line-chart { width:100%; height:170px; overflow:visible; stroke:none; }
  .gridline { stroke:var(--border-bright); stroke-dasharray:3 5; stroke-width:1; }
  .trend-line { stroke:var(--accent); stroke-width:2.5; vector-effect:non-scaling-stroke; }
  .plot-point { fill:var(--accent); stroke:var(--bg-panel); stroke-width:2; opacity:0; cursor:default; }
  .plot-point:hover,.plot-point:focus { opacity:1; outline:none; }
  .chart-dates { display:flex; justify-content:space-between; font-size:10px; color:var(--text-dim); padding:0 3px; }
  .month-bars { display:flex; gap:12px; height:215px; padding-top:16px; }
  .month-column { display:flex; flex:1; min-width:0; flex-direction:column; align-items:center; gap:8px; color:var(--text-dim); font-size:10px; }
  .bar-value { font-variant-numeric:tabular-nums; }
  .bar-track { display:flex; align-items:flex-end; height:154px; width:100%; max-width:38px; }
  .bar { width:100%; border-radius:5px 5px 2px 2px; background:color-mix(in srgb,var(--accent) 24%,var(--bg-raised)); }
  .bar.current { background:var(--accent); }
  .rankings { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:16px; }
  .count-pill { border:1px solid var(--border); border-radius:6px; padding:2px 7px; font-size:11px; color:var(--text-dim); }
  .ranking-list { display:flex; flex-direction:column; gap:18px; }
  .ranking-row { display:flex; gap:14px; align-items:center; }
  .rank,.model-mark { font-size:11px; color:var(--text-dim); font-variant-numeric:tabular-nums; flex:0 0 18px; }
  .model-mark { font-size:22px; color:var(--accent); }
  .ranking-info { flex:1; min-width:0; }
  .ranking-title strong { font-size:12px; font-weight:500; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
  .ranking-title > span { flex-shrink:0; font-variant-numeric:tabular-nums; font-size:12px; }
  .ranking-title small { color:var(--text-dim); font-size:10px; }
  .meter { height:4px; background:var(--bg-raised); border-radius:3px; margin-top:8px; overflow:hidden; }
  .meter span { display:block; background:var(--accent); height:100%; border-radius:inherit; }
  .meter.muted span { background:color-mix(in srgb,var(--accent) 55%,var(--text-dim)); }
  .no-ranking { color:var(--text-dim); font-size:12px; margin:15px 0 20px; }
  .coverage { display:flex; gap:9px; align-items:flex-start; margin-top:20px; color:var(--text-dim); font-size:11px; }
  .coverage p { margin:0; line-height:1.6; }
  .coverage strong { color:var(--text); font-weight:500; }
  .coverage-mark { border:1px solid var(--border-bright); border-radius:50%; width:14px; height:14px; text-align:center; flex-shrink:0; margin-top:2px; font-size:10px; }
  .empty-state { display:flex; gap:16px; align-items:center; padding:18px 20px; margin-bottom:20px; border:1px dashed var(--border-bright); border-radius:var(--radius); }
  .empty-state p { margin:4px 0 0; color:var(--text-dim); font-size:12px; }
  .empty-state .btn { margin-left:auto; flex-shrink:0; }
  .empty-mark { font-size:28px; color:var(--accent); }
  .sr-only { position:absolute; width:1px; height:1px; overflow:hidden; clip:rect(0,0,0,0); }
  @media(max-width:1050px) { .metrics { grid-template-columns:repeat(2,minmax(0,1fr)); } .charts { grid-template-columns:1fr; } .month-bars { height:175px; } .bar-track { height:120px; } }
  @media(max-width:760px) { .rankings { grid-template-columns:1fr; } .band-caption { display:none; } .dashboard-heading { align-items:flex-start; } .panel,.metric { padding:16px; } .privacy { font-size:10px; } .empty-mark { display:none; } }
</style>
