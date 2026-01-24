import { ScheduledEvent } from 'aws-lambda';
import {
  S3Client,
  ListObjectsV2Command,
  GetObjectCommand,
  PutObjectCommand,
} from '@aws-sdk/client-s3';

const s3 = new S3Client({});
const BUCKET = process.env.ANALYTICS_BUCKET || '';

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

interface SkillCandidate {
  pattern: string;
  count: number;
  suggested_skill: string;
  examples: string[];
}

// Model pricing (per million tokens)
const MODEL_PRICING: Record<string, { input: number; output: number }> = {
  sonnet: { input: 3.0, output: 15.0 },
  opus: { input: 15.0, output: 75.0 },
  haiku: { input: 0.25, output: 1.25 },
};

export async function handler(event: ScheduledEvent): Promise<void> {
  console.log('Analytics pipeline triggered:', event);

  try {
    // Process yesterday's data (run at 2 AM, process previous day)
    const yesterday = new Date();
    yesterday.setDate(yesterday.getDate() - 1);
    const dateStr = yesterday.toISOString().split('T')[0];
    const [year, month, day] = dateStr.split('-');

    console.log(`Processing analytics for date: ${dateStr}`);

    // Get all profiles from prompts
    const profiles = await discoverProfiles();
    console.log(`Found profiles: ${profiles.join(', ')}`);

    // Process each profile
    for (const profile of profiles) {
      await processProfile(profile, year, month, day, dateStr);
    }

    // Generate skill opportunities across all profiles
    await generateSkillOpportunities();

    console.log('Analytics pipeline completed');
  } catch (error) {
    console.error('Analytics pipeline error:', error);
    throw error;
  }
}

async function discoverProfiles(): Promise<string[]> {
  const profiles = new Set<string>();

  try {
    const result = await s3.send(new ListObjectsV2Command({
      Bucket: BUCKET,
      Prefix: 'prompts/',
      Delimiter: '/',
    }));

    for (const prefix of result.CommonPrefixes || []) {
      if (prefix.Prefix) {
        const profile = prefix.Prefix.replace('prompts/', '').replace('/', '');
        if (profile) profiles.add(profile);
      }
    }
  } catch (e) {
    console.error('Error discovering profiles:', e);
  }

  return Array.from(profiles);
}

async function processProfile(
  profile: string,
  year: string,
  month: string,
  day: string,
  dateStr: string
): Promise<void> {
  console.log(`Processing profile: ${profile} for ${dateStr}`);

  // Collect prompts for the day
  const prompts = await collectPrompts(profile, year, month, day);
  console.log(`Found ${prompts.length} prompts for ${profile}`);

  // Collect feedback for the day
  const feedback = await collectFeedback(profile, year, month, day);
  console.log(`Found ${feedback.length} feedback entries for ${profile}`);

  // Calculate metrics
  const aggregate = calculateAggregate(profile, dateStr, prompts, feedback);

  // Save aggregate
  await saveAggregate(profile, dateStr, aggregate);
}

async function collectPrompts(
  profile: string,
  year: string,
  month: string,
  day: string
): Promise<PromptRecord[]> {
  const prompts: PromptRecord[] = [];
  const prefix = `prompts/${profile}/${year}/${month}/${day}/`;

  try {
    const result = await s3.send(new ListObjectsV2Command({
      Bucket: BUCKET,
      Prefix: prefix,
    }));

    for (const obj of result.Contents || []) {
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
        console.error(`Error reading prompt ${obj.Key}:`, e);
      }
    }
  } catch (e) {
    console.error(`Error listing prompts for ${profile}:`, e);
  }

  return prompts;
}

async function collectFeedback(
  profile: string,
  year: string,
  month: string,
  day: string
): Promise<FeedbackRecord[]> {
  const feedback: FeedbackRecord[] = [];
  const prefix = `feedback/${profile}/${year}/${month}/${day}/`;

  try {
    const result = await s3.send(new ListObjectsV2Command({
      Bucket: BUCKET,
      Prefix: prefix,
    }));

    for (const obj of result.Contents || []) {
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
          feedback.push(...record);
        } else {
          feedback.push(record);
        }
      } catch (e) {
        console.error(`Error reading feedback ${obj.Key}:`, e);
      }
    }
  } catch (e) {
    console.error(`Error listing feedback for ${profile}:`, e);
  }

  return feedback;
}

function calculateAggregate(
  profile: string,
  dateStr: string,
  prompts: PromptRecord[],
  feedback: FeedbackRecord[]
): DailyAggregate {
  let totalTokensIn = 0;
  let totalTokensOut = 0;
  let totalCost = 0;
  let totalTurns = 0;
  const prefixCounts: Record<string, number> = {};
  const toolCounts: Record<string, number> = {};

  for (const prompt of prompts) {
    // Token counts
    totalTokensIn += prompt.prompt?.tokens || 0;
    totalTokensOut += prompt.outcome?.total_output_tokens || 0;

    // Turns
    totalTurns += prompt.outcome?.next_turn_count || 1;

    // Cost calculation
    const model = (prompt.context?.model || 'sonnet').toLowerCase();
    const pricing = MODEL_PRICING[model] || MODEL_PRICING.sonnet;
    totalCost += (prompt.prompt?.tokens || 0) * pricing.input / 1_000_000;
    totalCost += (prompt.outcome?.total_output_tokens || 0) * pricing.output / 1_000_000;

    // Extract prefix patterns
    const text = prompt.prompt?.text || '';
    const prefix = extractPrefix(text);
    if (prefix) {
      prefixCounts[prefix] = (prefixCounts[prefix] || 0) + 1;
    }

    // Track tool usage
    for (const tool of prompt.outcome?.tools_used || []) {
      toolCounts[tool] = (toolCounts[tool] || 0) + 1;
    }
  }

  // Count feedback
  let feedbackPositive = 0;
  let feedbackNegative = 0;
  for (const fb of feedback) {
    if (fb.rating === 'positive') feedbackPositive++;
    else if (fb.rating === 'negative') feedbackNegative++;
  }

  // Sort prefixes by count
  const commonPrefixes = Object.entries(prefixCounts)
    .map(([prefix, count]) => ({ prefix, count }))
    .sort((a, b) => b.count - a.count)
    .slice(0, 20);

  // Identify skill candidates
  const skillCandidates = identifySkillCandidates(prompts);

  return {
    date: dateStr,
    profile,
    metrics: {
      total_prompts: prompts.length,
      total_tokens_in: totalTokensIn,
      total_tokens_out: totalTokensOut,
      total_cost_usd: Math.round(totalCost * 100) / 100,
      avg_turns_per_task: prompts.length > 0 ? totalTurns / prompts.length : 0,
      feedback_positive: feedbackPositive,
      feedback_negative: feedbackNegative,
    },
    patterns: {
      common_prefixes: commonPrefixes,
      skill_candidates: skillCandidates,
    },
  };
}

function extractPrefix(text: string): string | null {
  // Extract the first meaningful word(s) as a prefix
  const match = text.match(/^(\w+(?:\s+\w+)?)/);
  if (match) {
    const prefix = match[1].toLowerCase();
    // Filter out common non-actionable words
    const skipWords = ['the', 'a', 'an', 'i', 'we', 'you', 'it', 'this', 'that', 'please', 'can', 'could', 'would'];
    const words = prefix.split(' ');
    if (skipWords.includes(words[0])) {
      return words.length > 1 ? words[1] : null;
    }
    return words[0];
  }
  return null;
}

function identifySkillCandidates(prompts: PromptRecord[]): SkillCandidate[] {
  const patterns: Record<string, { count: number; examples: string[] }> = {};

  // Pattern templates to look for
  const patternTemplates = [
    { regex: /^(create|add|implement)\s+(?:a\s+)?(\w+)/i, skill: (m: string[]) => `create-${m[2]}` },
    { regex: /^(fix|debug|resolve)\s+(?:the\s+)?(\w+)/i, skill: (m: string[]) => `fix-${m[2]}` },
    { regex: /^(deploy|push)\s+(?:to\s+)?(\w+)/i, skill: (m: string[]) => `deploy-${m[2]}` },
    { regex: /^(test|run tests)/i, skill: () => 'run-tests' },
    { regex: /^(refactor|clean up)/i, skill: () => 'refactor' },
    { regex: /^(update|upgrade)\s+(?:the\s+)?(\w+)/i, skill: (m: string[]) => `update-${m[2]}` },
    { regex: /^(generate|scaffold)/i, skill: () => 'generate' },
    { regex: /^(review|check)\s+(?:the\s+)?(\w+)/i, skill: (m: string[]) => `review-${m[2]}` },
  ];

  for (const prompt of prompts) {
    const text = prompt.prompt?.text || '';

    for (const template of patternTemplates) {
      const match = text.match(template.regex);
      if (match) {
        const skillName = template.skill(match);
        const patternKey = match[0].toLowerCase();

        if (!patterns[patternKey]) {
          patterns[patternKey] = { count: 0, examples: [] };
        }
        patterns[patternKey].count++;
        if (patterns[patternKey].examples.length < 3) {
          patterns[patternKey].examples.push(text.substring(0, 100));
        }
      }
    }
  }

  // Filter to patterns that appear multiple times
  return Object.entries(patterns)
    .filter(([_, data]) => data.count >= 3)
    .map(([pattern, data]) => ({
      pattern,
      count: data.count,
      suggested_skill: pattern.split(' ')[0] + '-skill',
      examples: data.examples,
    }))
    .sort((a, b) => b.count - a.count)
    .slice(0, 10);
}

async function saveAggregate(profile: string, dateStr: string, aggregate: DailyAggregate): Promise<void> {
  const key = `aggregates/daily/${profile}/${dateStr}.json`;

  try {
    await s3.send(new PutObjectCommand({
      Bucket: BUCKET,
      Key: key,
      Body: JSON.stringify(aggregate, null, 2),
      ContentType: 'application/json',
    }));
    console.log(`Saved aggregate: ${key}`);
  } catch (e) {
    console.error(`Error saving aggregate ${key}:`, e);
  }
}

async function generateSkillOpportunities(): Promise<void> {
  console.log('Generating skill opportunities...');

  const allSkills: SkillCandidate[] = [];

  // Collect skill candidates from all recent aggregates
  try {
    const result = await s3.send(new ListObjectsV2Command({
      Bucket: BUCKET,
      Prefix: 'aggregates/daily/',
    }));

    // Get last 30 days of aggregates
    const recentObjects = (result.Contents || [])
      .sort((a, b) => (b.LastModified?.getTime() || 0) - (a.LastModified?.getTime() || 0))
      .slice(0, 100);

    for (const obj of recentObjects) {
      if (!obj.Key) continue;

      try {
        const getResult = await s3.send(new GetObjectCommand({
          Bucket: BUCKET,
          Key: obj.Key,
        }));

        const body = await getResult.Body?.transformToString();
        if (!body) continue;

        const aggregate: DailyAggregate = JSON.parse(body);
        allSkills.push(...(aggregate.patterns?.skill_candidates || []));
      } catch (e) {
        console.error(`Error reading aggregate ${obj.Key}:`, e);
      }
    }
  } catch (e) {
    console.error('Error listing aggregates:', e);
  }

  // Merge and deduplicate skills
  const mergedSkills: Record<string, SkillCandidate> = {};
  for (const skill of allSkills) {
    const key = skill.pattern.toLowerCase();
    if (!mergedSkills[key]) {
      mergedSkills[key] = { ...skill };
    } else {
      mergedSkills[key].count += skill.count;
      // Add unique examples
      for (const ex of skill.examples) {
        if (mergedSkills[key].examples.length < 5 && !mergedSkills[key].examples.includes(ex)) {
          mergedSkills[key].examples.push(ex);
        }
      }
    }
  }

  // Sort by count and take top 20
  const topSkills = Object.values(mergedSkills)
    .sort((a, b) => b.count - a.count)
    .slice(0, 20);

  // Save to patterns/skills/
  const key = 'patterns/skills/identified_skills.json';
  try {
    await s3.send(new PutObjectCommand({
      Bucket: BUCKET,
      Key: key,
      Body: JSON.stringify(topSkills, null, 2),
      ContentType: 'application/json',
    }));
    console.log(`Saved ${topSkills.length} skill opportunities to ${key}`);
  } catch (e) {
    console.error(`Error saving skills ${key}:`, e);
  }
}
