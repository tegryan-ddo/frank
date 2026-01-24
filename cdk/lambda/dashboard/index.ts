import { ALBEvent, ALBResult } from 'aws-lambda';
import { S3Client, ListObjectsV2Command, GetObjectCommand } from '@aws-sdk/client-s3';
import { SSMClient, GetParameterCommand } from '@aws-sdk/client-ssm';

const s3 = new S3Client({});
const ssm = new SSMClient({});

const BUCKET = process.env.ANALYTICS_BUCKET || '';
const PROFILES_PARAM = process.env.PROFILES_PARAM || '/frank/profiles';

interface PromptRecord {
  id: string;
  profile: string;
  session_id: string;
  timestamp: string;
  prompt: {
    text: string;
    tokens: number;
  };
  context: {
    turn_number: number;
    model: string;
    files_referenced: string[];
  };
  outcome: {
    next_turn_count: number;
    tools_used: string[];
    total_output_tokens: number;
  };
}

interface FeedbackRecord {
  prompt_id: string;
  profile: string;
  timestamp: string;
  rating: 'positive' | 'negative';
}

interface DailyAggregate {
  date: string;
  profile: string;
  metrics: {
    total_prompts: number;
    total_tokens_in: number;
    total_tokens_out: number;
    total_cost_usd: number;
    avg_turns_per_task: number;
    feedback_positive: number;
    feedback_negative: number;
  };
  patterns: {
    common_prefixes: { prefix: string; count: number }[];
    skill_candidates: { pattern: string; count: number; suggested_skill: string }[];
  };
}

export async function handler(event: ALBEvent): Promise<ALBResult> {
  console.log('Dashboard request:', event.path);

  const path = event.path || '/dashboard';

  try {
    // API endpoints
    if (path.startsWith('/dashboard/api/')) {
      return await handleApi(event);
    }

    // Main dashboard page
    return {
      statusCode: 200,
      headers: {
        'Content-Type': 'text/html; charset=utf-8',
        'Cache-Control': 'no-cache',
      },
      body: getDashboardHtml(),
    };
  } catch (error) {
    console.error('Dashboard error:', error);
    return {
      statusCode: 500,
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ error: 'Internal server error' }),
    };
  }
}

async function handleApi(event: ALBEvent): Promise<ALBResult> {
  const path = event.path || '';
  const params = event.queryStringParameters || {};

  const headers = {
    'Content-Type': 'application/json',
    'Access-Control-Allow-Origin': '*',
  };

  // GET /dashboard/api/profiles - List profiles
  if (path === '/dashboard/api/profiles') {
    const profiles = await getProfiles();
    return { statusCode: 200, headers, body: JSON.stringify(profiles) };
  }

  // GET /dashboard/api/metrics - Get aggregate metrics
  if (path === '/dashboard/api/metrics') {
    const profile = params.profile || 'all';
    const days = parseInt(params.days || '30');
    const metrics = await getMetrics(profile, days);
    return { statusCode: 200, headers, body: JSON.stringify(metrics) };
  }

  // GET /dashboard/api/prompts - List prompts
  if (path === '/dashboard/api/prompts') {
    const profile = params.profile || 'all';
    const date = params.date || new Date().toISOString().split('T')[0];
    const prompts = await getPrompts(profile, date);
    return { statusCode: 200, headers, body: JSON.stringify(prompts) };
  }

  // GET /dashboard/api/skills - Get skill opportunities
  if (path === '/dashboard/api/skills') {
    const skills = await getSkillOpportunities();
    return { statusCode: 200, headers, body: JSON.stringify(skills) };
  }

  // GET /dashboard/api/effectiveness - Get effectiveness report
  if (path === '/dashboard/api/effectiveness') {
    const profile = params.profile || 'all';
    const effectiveness = await getEffectivenessReport(profile);
    return { statusCode: 200, headers, body: JSON.stringify(effectiveness) };
  }

  return { statusCode: 404, headers, body: JSON.stringify({ error: 'Not found' }) };
}

async function getProfiles(): Promise<string[]> {
  try {
    const result = await ssm.send(new GetParameterCommand({ Name: PROFILES_PARAM }));
    const profiles = JSON.parse(result.Parameter?.Value || '[]');
    return profiles.map((p: any) => p.name || p);
  } catch {
    return [];
  }
}

async function getMetrics(profile: string, days: number): Promise<any> {
  const metrics = {
    total_prompts: 0,
    total_tokens_in: 0,
    total_tokens_out: 0,
    total_cost_usd: 0,
    avg_turns_per_task: 0,
    feedback_positive: 0,
    feedback_negative: 0,
    prompts_by_day: [] as { date: string; count: number }[],
    tools_usage: {} as Record<string, number>,
  };

  // List aggregates for the date range
  const endDate = new Date();
  const startDate = new Date();
  startDate.setDate(startDate.getDate() - days);

  const prefix = profile === 'all' ? 'aggregates/daily/' : `aggregates/daily/${profile}/`;

  try {
    const listResult = await s3.send(new ListObjectsV2Command({
      Bucket: BUCKET,
      Prefix: prefix,
    }));

    for (const obj of listResult.Contents || []) {
      if (!obj.Key) continue;

      try {
        const getResult = await s3.send(new GetObjectCommand({
          Bucket: BUCKET,
          Key: obj.Key,
        }));

        const body = await getResult.Body?.transformToString();
        if (!body) continue;

        const aggregate: DailyAggregate = JSON.parse(body);
        const aggDate = new Date(aggregate.date);

        if (aggDate >= startDate && aggDate <= endDate) {
          metrics.total_prompts += aggregate.metrics.total_prompts;
          metrics.total_tokens_in += aggregate.metrics.total_tokens_in;
          metrics.total_tokens_out += aggregate.metrics.total_tokens_out;
          metrics.total_cost_usd += aggregate.metrics.total_cost_usd;
          metrics.feedback_positive += aggregate.metrics.feedback_positive;
          metrics.feedback_negative += aggregate.metrics.feedback_negative;

          metrics.prompts_by_day.push({
            date: aggregate.date,
            count: aggregate.metrics.total_prompts,
          });
        }
      } catch (e) {
        console.error('Error reading aggregate:', obj.Key, e);
      }
    }

    // Calculate average turns
    if (metrics.total_prompts > 0) {
      metrics.avg_turns_per_task = metrics.total_tokens_out / metrics.total_tokens_in;
    }

    // Sort prompts by day
    metrics.prompts_by_day.sort((a, b) => a.date.localeCompare(b.date));

  } catch (e) {
    console.error('Error fetching metrics:', e);
  }

  return metrics;
}

async function getPrompts(profile: string, date: string): Promise<PromptRecord[]> {
  const prompts: PromptRecord[] = [];
  const [year, month, day] = date.split('-');

  const prefix = profile === 'all'
    ? `prompts/`
    : `prompts/${profile}/${year}/${month}/${day}/`;

  try {
    const listResult = await s3.send(new ListObjectsV2Command({
      Bucket: BUCKET,
      Prefix: prefix,
      MaxKeys: 100,
    }));

    for (const obj of listResult.Contents || []) {
      if (!obj.Key) continue;

      try {
        const getResult = await s3.send(new GetObjectCommand({
          Bucket: BUCKET,
          Key: obj.Key,
        }));

        const body = await getResult.Body?.transformToString();
        if (!body) continue;

        const record = JSON.parse(body);
        if (Array.isArray(record)) {
          prompts.push(...record);
        } else {
          prompts.push(record);
        }
      } catch (e) {
        console.error('Error reading prompt:', obj.Key, e);
      }
    }
  } catch (e) {
    console.error('Error listing prompts:', e);
  }

  return prompts.sort((a, b) => b.timestamp.localeCompare(a.timestamp));
}

async function getSkillOpportunities(): Promise<any[]> {
  const skills: any[] = [];

  try {
    // Look for the latest patterns file
    const listResult = await s3.send(new ListObjectsV2Command({
      Bucket: BUCKET,
      Prefix: 'patterns/skills/',
    }));

    for (const obj of listResult.Contents || []) {
      if (!obj.Key) continue;

      try {
        const getResult = await s3.send(new GetObjectCommand({
          Bucket: BUCKET,
          Key: obj.Key,
        }));

        const body = await getResult.Body?.transformToString();
        if (!body) continue;

        const patterns = JSON.parse(body);
        if (Array.isArray(patterns)) {
          skills.push(...patterns);
        }
      } catch (e) {
        console.error('Error reading skills:', obj.Key, e);
      }
    }
  } catch (e) {
    console.error('Error fetching skills:', e);
  }

  return skills;
}

async function getEffectivenessReport(profile: string): Promise<any> {
  const report = {
    efficient_prompts: [] as any[],
    improvement_needed: [] as any[],
    tool_effectiveness: {} as Record<string, { success_rate: number; avg_turns: number }>,
    prompt_length_analysis: {
      short: { avg_turns: 0, count: 0 },
      medium: { avg_turns: 0, count: 0 },
      long: { avg_turns: 0, count: 0 },
    },
  };

  // Get recent prompts for analysis
  const today = new Date().toISOString().split('T')[0];
  const prompts = await getPrompts(profile, today);

  for (const prompt of prompts) {
    const turns = prompt.outcome?.next_turn_count || 1;
    const promptLength = prompt.prompt?.text?.length || 0;

    // Categorize by efficiency
    if (turns <= 2) {
      report.efficient_prompts.push({
        text: prompt.prompt?.text?.substring(0, 100),
        turns,
        tools: prompt.outcome?.tools_used,
      });
    } else if (turns >= 5) {
      report.improvement_needed.push({
        text: prompt.prompt?.text?.substring(0, 100),
        turns,
        tools: prompt.outcome?.tools_used,
      });
    }

    // Categorize by length
    if (promptLength < 50) {
      report.prompt_length_analysis.short.count++;
      report.prompt_length_analysis.short.avg_turns += turns;
    } else if (promptLength < 200) {
      report.prompt_length_analysis.medium.count++;
      report.prompt_length_analysis.medium.avg_turns += turns;
    } else {
      report.prompt_length_analysis.long.count++;
      report.prompt_length_analysis.long.avg_turns += turns;
    }

    // Track tool effectiveness
    for (const tool of prompt.outcome?.tools_used || []) {
      if (!report.tool_effectiveness[tool]) {
        report.tool_effectiveness[tool] = { success_rate: 0, avg_turns: 0 };
      }
      report.tool_effectiveness[tool].avg_turns += turns;
    }
  }

  // Calculate averages
  for (const key of ['short', 'medium', 'long'] as const) {
    if (report.prompt_length_analysis[key].count > 0) {
      report.prompt_length_analysis[key].avg_turns /= report.prompt_length_analysis[key].count;
    }
  }

  return report;
}

function getDashboardHtml(): string {
  return `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Frank Analytics Dashboard</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
            background: #1e1e1e;
            color: #d4d4d4;
            min-height: 100vh;
        }
        .header {
            background: #252526;
            padding: 16px 24px;
            border-bottom: 1px solid #3c3c3c;
            display: flex;
            align-items: center;
            justify-content: space-between;
        }
        .header h1 { font-size: 20px; color: #cccccc; }
        .header-controls {
            display: flex;
            gap: 12px;
            align-items: center;
        }
        select, input {
            background: #3c3c3c;
            border: 1px solid #5c5c5c;
            color: #d4d4d4;
            padding: 8px 12px;
            border-radius: 4px;
            font-size: 14px;
        }
        .container { padding: 24px; max-width: 1400px; margin: 0 auto; }
        .tabs {
            display: flex;
            gap: 4px;
            margin-bottom: 24px;
            border-bottom: 1px solid #3c3c3c;
            padding-bottom: 12px;
        }
        .tab {
            padding: 8px 16px;
            background: transparent;
            border: none;
            color: #9d9d9d;
            cursor: pointer;
            font-size: 14px;
            border-radius: 4px;
        }
        .tab:hover { background: #3c3c3c; color: #ffffff; }
        .tab.active { background: #0e639c; color: #ffffff; }
        .tab-content { display: none; }
        .tab-content.active { display: block; }
        .metrics-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
            gap: 16px;
            margin-bottom: 24px;
        }
        .metric-card {
            background: #252526;
            padding: 20px;
            border-radius: 8px;
            border: 1px solid #3c3c3c;
        }
        .metric-label { font-size: 12px; color: #9d9d9d; text-transform: uppercase; margin-bottom: 8px; }
        .metric-value { font-size: 32px; font-weight: 600; color: #4ec9b0; }
        .metric-value.cost { color: #dcdcaa; }
        .metric-value.feedback { color: #6a9955; }
        .chart-container {
            background: #252526;
            padding: 20px;
            border-radius: 8px;
            border: 1px solid #3c3c3c;
            margin-bottom: 24px;
        }
        .chart-title { font-size: 14px; color: #cccccc; margin-bottom: 16px; }
        .chart-placeholder {
            height: 200px;
            background: #1e1e1e;
            border-radius: 4px;
            display: flex;
            align-items: center;
            justify-content: center;
            color: #6d6d6d;
        }
        table {
            width: 100%;
            border-collapse: collapse;
            background: #252526;
            border-radius: 8px;
            overflow: hidden;
        }
        th, td {
            padding: 12px 16px;
            text-align: left;
            border-bottom: 1px solid #3c3c3c;
        }
        th { background: #2d2d2d; color: #cccccc; font-weight: 500; font-size: 12px; text-transform: uppercase; }
        td { font-size: 14px; }
        .prompt-text {
            max-width: 400px;
            overflow: hidden;
            text-overflow: ellipsis;
            white-space: nowrap;
            color: #4ec9b0;
        }
        .badge {
            display: inline-block;
            padding: 2px 8px;
            border-radius: 10px;
            font-size: 11px;
            font-weight: 500;
        }
        .badge.positive { background: #2d5a2d; color: #6a9955; }
        .badge.negative { background: #5a2d2d; color: #f44747; }
        .badge.tool { background: #3c3c3c; color: #dcdcaa; margin-right: 4px; }
        .skill-card {
            background: #252526;
            padding: 16px;
            border-radius: 8px;
            border: 1px solid #3c3c3c;
            margin-bottom: 12px;
        }
        .skill-pattern { color: #4ec9b0; font-family: monospace; margin-bottom: 8px; }
        .skill-meta { display: flex; gap: 16px; font-size: 12px; color: #9d9d9d; }
        .loading { text-align: center; padding: 40px; color: #6d6d6d; }
        .bar-chart { display: flex; flex-direction: column; gap: 8px; }
        .bar-row { display: flex; align-items: center; gap: 12px; }
        .bar-label { width: 100px; font-size: 12px; color: #9d9d9d; }
        .bar-container { flex: 1; height: 24px; background: #1e1e1e; border-radius: 4px; overflow: hidden; }
        .bar-fill { height: 100%; background: linear-gradient(90deg, #0e639c, #4ec9b0); transition: width 0.3s; }
        .bar-value { width: 60px; text-align: right; font-size: 12px; color: #cccccc; }
        .empty-state { text-align: center; padding: 60px; color: #6d6d6d; }
        .empty-state h3 { margin-bottom: 12px; color: #9d9d9d; }
        .back-link {
            color: #4ec9b0;
            text-decoration: none;
            font-size: 14px;
        }
        .back-link:hover { text-decoration: underline; }
    </style>
</head>
<body>
    <div class="header">
        <div style="display: flex; align-items: center; gap: 16px;">
            <a href="/" class="back-link">&larr; Back to Frank</a>
            <h1>Analytics Dashboard</h1>
        </div>
        <div class="header-controls">
            <select id="profile-select">
                <option value="all">All Profiles</option>
            </select>
            <select id="date-range">
                <option value="7">Last 7 days</option>
                <option value="30" selected>Last 30 days</option>
                <option value="90">Last 90 days</option>
            </select>
        </div>
    </div>

    <div class="container">
        <div class="tabs">
            <button class="tab active" data-tab="overview">Overview</button>
            <button class="tab" data-tab="prompts">Prompts</button>
            <button class="tab" data-tab="skills">Skill Opportunities</button>
            <button class="tab" data-tab="effectiveness">Effectiveness</button>
        </div>

        <!-- Overview Tab -->
        <div class="tab-content active" id="overview">
            <div class="metrics-grid" id="metrics-grid">
                <div class="metric-card">
                    <div class="metric-label">Total Prompts</div>
                    <div class="metric-value" id="total-prompts">-</div>
                </div>
                <div class="metric-card">
                    <div class="metric-label">Total Cost</div>
                    <div class="metric-value cost" id="total-cost">-</div>
                </div>
                <div class="metric-card">
                    <div class="metric-label">Positive Feedback</div>
                    <div class="metric-value feedback" id="positive-feedback">-</div>
                </div>
                <div class="metric-card">
                    <div class="metric-label">Feedback Ratio</div>
                    <div class="metric-value" id="feedback-ratio">-</div>
                </div>
            </div>

            <div class="chart-container">
                <div class="chart-title">Prompts Over Time</div>
                <div id="prompts-chart" class="bar-chart"></div>
            </div>

            <div class="chart-container">
                <div class="chart-title">Tool Usage</div>
                <div id="tools-chart" class="bar-chart"></div>
            </div>
        </div>

        <!-- Prompts Tab -->
        <div class="tab-content" id="prompts">
            <table id="prompts-table">
                <thead>
                    <tr>
                        <th>Time</th>
                        <th>Prompt</th>
                        <th>Turns</th>
                        <th>Tools</th>
                        <th>Feedback</th>
                    </tr>
                </thead>
                <tbody id="prompts-tbody">
                    <tr><td colspan="5" class="loading">Loading prompts...</td></tr>
                </tbody>
            </table>
        </div>

        <!-- Skills Tab -->
        <div class="tab-content" id="skills">
            <div id="skills-list">
                <div class="loading">Loading skill opportunities...</div>
            </div>
        </div>

        <!-- Effectiveness Tab -->
        <div class="tab-content" id="effectiveness">
            <div class="metrics-grid">
                <div class="metric-card">
                    <div class="metric-label">Efficient Prompts (1-2 turns)</div>
                    <div class="metric-value" id="efficient-count">-</div>
                </div>
                <div class="metric-card">
                    <div class="metric-label">Need Improvement (5+ turns)</div>
                    <div class="metric-value cost" id="improvement-count">-</div>
                </div>
            </div>

            <div class="chart-container">
                <div class="chart-title">Prompt Length vs Turns</div>
                <div id="length-chart" class="bar-chart"></div>
            </div>

            <div class="chart-container">
                <div class="chart-title">Prompts Needing Improvement</div>
                <div id="improvement-list"></div>
            </div>
        </div>
    </div>

    <script>
        // Tab switching
        document.querySelectorAll('.tab').forEach(tab => {
            tab.addEventListener('click', () => {
                document.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
                document.querySelectorAll('.tab-content').forEach(c => c.classList.remove('active'));
                tab.classList.add('active');
                document.getElementById(tab.dataset.tab).classList.add('active');
            });
        });

        // Load profiles
        async function loadProfiles() {
            try {
                const res = await fetch('/dashboard/api/profiles');
                const profiles = await res.json();
                const select = document.getElementById('profile-select');
                profiles.forEach(p => {
                    const opt = document.createElement('option');
                    opt.value = p;
                    opt.textContent = p;
                    select.appendChild(opt);
                });
            } catch (e) {
                console.error('Failed to load profiles:', e);
            }
        }

        // Load metrics
        async function loadMetrics() {
            const profile = document.getElementById('profile-select').value;
            const days = document.getElementById('date-range').value;

            try {
                const res = await fetch(\`/dashboard/api/metrics?profile=\${profile}&days=\${days}\`);
                const data = await res.json();

                document.getElementById('total-prompts').textContent = data.total_prompts.toLocaleString();
                document.getElementById('total-cost').textContent = '$' + data.total_cost_usd.toFixed(2);
                document.getElementById('positive-feedback').textContent = data.feedback_positive;

                const total = data.feedback_positive + data.feedback_negative;
                const ratio = total > 0 ? ((data.feedback_positive / total) * 100).toFixed(0) + '%' : '-';
                document.getElementById('feedback-ratio').textContent = ratio;

                // Render prompts chart
                renderBarChart('prompts-chart', data.prompts_by_day.slice(-14).map(d => ({
                    label: d.date.split('-').slice(1).join('/'),
                    value: d.count,
                })));

                // Render tools chart (placeholder)
                const toolsData = Object.entries(data.tools_usage || {}).map(([k, v]) => ({
                    label: k,
                    value: v,
                }));
                if (toolsData.length > 0) {
                    renderBarChart('tools-chart', toolsData.slice(0, 10));
                }
            } catch (e) {
                console.error('Failed to load metrics:', e);
            }
        }

        // Load prompts
        async function loadPrompts() {
            const profile = document.getElementById('profile-select').value;
            const today = new Date().toISOString().split('T')[0];

            try {
                const res = await fetch(\`/dashboard/api/prompts?profile=\${profile}&date=\${today}\`);
                const prompts = await res.json();

                const tbody = document.getElementById('prompts-tbody');
                if (prompts.length === 0) {
                    tbody.innerHTML = '<tr><td colspan="5" class="empty-state"><h3>No prompts yet</h3><p>Prompts will appear here once you start using Frank</p></td></tr>';
                    return;
                }

                tbody.innerHTML = prompts.slice(0, 50).map(p => \`
                    <tr>
                        <td>\${new Date(p.timestamp).toLocaleTimeString()}</td>
                        <td class="prompt-text" title="\${escapeHtml(p.prompt?.text || '')}">\${escapeHtml((p.prompt?.text || '').substring(0, 80))}</td>
                        <td>\${p.outcome?.next_turn_count || '-'}</td>
                        <td>\${(p.outcome?.tools_used || []).slice(0, 3).map(t => '<span class="badge tool">' + t + '</span>').join('')}</td>
                        <td>-</td>
                    </tr>
                \`).join('');
            } catch (e) {
                console.error('Failed to load prompts:', e);
                document.getElementById('prompts-tbody').innerHTML = '<tr><td colspan="5" class="loading">Error loading prompts</td></tr>';
            }
        }

        // Load skills
        async function loadSkills() {
            try {
                const res = await fetch('/dashboard/api/skills');
                const skills = await res.json();

                const container = document.getElementById('skills-list');
                if (skills.length === 0) {
                    container.innerHTML = '<div class="empty-state"><h3>No skill opportunities detected yet</h3><p>As you use Frank more, patterns will emerge that suggest potential skills to create</p></div>';
                    return;
                }

                container.innerHTML = skills.map(s => \`
                    <div class="skill-card">
                        <div class="skill-pattern">"\${escapeHtml(s.pattern)}"</div>
                        <div class="skill-meta">
                            <span>Frequency: \${s.count}</span>
                            <span>Suggested: <strong>\${s.suggested_skill}</strong></span>
                        </div>
                    </div>
                \`).join('');
            } catch (e) {
                console.error('Failed to load skills:', e);
            }
        }

        // Load effectiveness
        async function loadEffectiveness() {
            const profile = document.getElementById('profile-select').value;

            try {
                const res = await fetch(\`/dashboard/api/effectiveness?profile=\${profile}\`);
                const data = await res.json();

                document.getElementById('efficient-count').textContent = data.efficient_prompts?.length || 0;
                document.getElementById('improvement-count').textContent = data.improvement_needed?.length || 0;

                // Render length analysis
                const lengthData = [
                    { label: 'Short (<50 chars)', value: data.prompt_length_analysis?.short?.avg_turns || 0 },
                    { label: 'Medium (50-200)', value: data.prompt_length_analysis?.medium?.avg_turns || 0 },
                    { label: 'Long (>200 chars)', value: data.prompt_length_analysis?.long?.avg_turns || 0 },
                ];
                renderBarChart('length-chart', lengthData);

                // Render improvement list
                const improvementList = document.getElementById('improvement-list');
                if (data.improvement_needed?.length > 0) {
                    improvementList.innerHTML = data.improvement_needed.slice(0, 10).map(p => \`
                        <div class="skill-card">
                            <div class="prompt-text">"\${escapeHtml(p.text || '')}"</div>
                            <div class="skill-meta">
                                <span>Turns: \${p.turns}</span>
                                <span>Tools: \${(p.tools || []).join(', ')}</span>
                            </div>
                        </div>
                    \`).join('');
                } else {
                    improvementList.innerHTML = '<div class="empty-state">No prompts needing improvement</div>';
                }
            } catch (e) {
                console.error('Failed to load effectiveness:', e);
            }
        }

        function renderBarChart(containerId, data) {
            const container = document.getElementById(containerId);
            if (!data || data.length === 0) {
                container.innerHTML = '<div class="empty-state">No data available</div>';
                return;
            }

            const max = Math.max(...data.map(d => d.value)) || 1;
            container.innerHTML = data.map(d => \`
                <div class="bar-row">
                    <div class="bar-label">\${d.label}</div>
                    <div class="bar-container">
                        <div class="bar-fill" style="width: \${(d.value / max) * 100}%"></div>
                    </div>
                    <div class="bar-value">\${typeof d.value === 'number' ? d.value.toFixed(1) : d.value}</div>
                </div>
            \`).join('');
        }

        function escapeHtml(text) {
            const div = document.createElement('div');
            div.textContent = text;
            return div.innerHTML;
        }

        // Event listeners
        document.getElementById('profile-select').addEventListener('change', () => {
            loadMetrics();
            loadPrompts();
            loadEffectiveness();
        });
        document.getElementById('date-range').addEventListener('change', loadMetrics);

        // Initial load
        loadProfiles();
        loadMetrics();
        loadPrompts();
        loadSkills();
        loadEffectiveness();
    </script>
</body>
</html>`;
}
