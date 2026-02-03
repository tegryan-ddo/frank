import {
  ECSClient,
  ListTasksCommand,
  DescribeTasksCommand,
  DescribeServicesCommand,
  RunTaskCommand,
  StopTaskCommand,
  Tag,
  Attachment,
  KeyValuePair,
} from '@aws-sdk/client-ecs';
import {
  ElasticLoadBalancingV2Client,
  DescribeTargetGroupsCommand,
  CreateTargetGroupCommand,
  DeleteTargetGroupCommand,
  DescribeRulesCommand,
  CreateRuleCommand,
  DeleteRuleCommand,
  RegisterTargetsCommand,
  DeregisterTargetsCommand,
  DescribeLoadBalancersCommand,
  DescribeListenersCommand,
  Listener,
} from '@aws-sdk/client-elastic-load-balancing-v2';
import {
  SSMClient,
  GetParameterCommand,
  PutParameterCommand,
} from '@aws-sdk/client-ssm';

const ecsClient = new ECSClient({});
const elbClient = new ElasticLoadBalancingV2Client({});
const ssmClient = new SSMClient({});

// Configuration from environment
const CLUSTER = process.env.ECS_CLUSTER || 'frank';
const SERVICE = process.env.ECS_SERVICE || 'FrankStack-FrankService';
const DOMAIN = process.env.DOMAIN || 'frank.digitaldevops.io';
const PROFILES_PARAM = process.env.PROFILES_PARAM || '/frank/profiles';
const ALB_NAME = process.env.ALB_NAME || 'frank-alb';

// Cognito config for profile route authentication
const COGNITO_USER_POOL_ARN = process.env.COGNITO_USER_POOL_ARN || '';
const COGNITO_CLIENT_ID = process.env.COGNITO_CLIENT_ID || '';
const COGNITO_DOMAIN = process.env.COGNITO_DOMAIN || '';

interface Profile {
  name: string;
  repo: string;
  branch?: string;
  description?: string;
  category?: string;
}

interface ProfileStatus extends Profile {
  status: 'running' | 'stopped';
  taskId?: string;
  url?: string;
  activeUsers?: number;
  users?: Array<{ display_name: string; short_id: string }>;
}

interface ApiResponse {
  statusCode: number;
  headers: Record<string, string>;
  body: string;
}

// CORS headers
const corsHeaders = {
  'Content-Type': 'application/json',
  'Access-Control-Allow-Origin': '*',
  'Access-Control-Allow-Methods': 'GET, POST, DELETE, OPTIONS',
  'Access-Control-Allow-Headers': 'Content-Type, Authorization',
};

export async function handler(event: any): Promise<ApiResponse> {
  console.log('Event:', JSON.stringify(event, null, 2));

  const path = event.path || event.rawPath || '';
  const method = event.httpMethod || event.requestContext?.http?.method || 'GET';

  // Handle CORS preflight
  if (method === 'OPTIONS') {
    return { statusCode: 200, headers: corsHeaders, body: '' };
  }

  try {
    // Serve launch page at root
    if ((path === '/' || path === '' || path === '/launch') && method === 'GET') {
      return serveLaunchPage();
    }

    // Route API requests
    if (path === '/api/profiles' && method === 'GET') {
      return await listProfiles();
    }

    const startMatch = path.match(/^\/api\/profiles\/([^/]+)\/start$/);
    if (startMatch && method === 'POST') {
      return await startProfile(startMatch[1]);
    }

    const stopMatch = path.match(/^\/api\/profiles\/([^/]+)\/stop$/);
    if (stopMatch && method === 'POST') {
      return await stopProfile(stopMatch[1]);
    }

    return {
      statusCode: 404,
      headers: corsHeaders,
      body: JSON.stringify({ error: 'Not found' }),
    };
  } catch (error: any) {
    console.error('Error:', error);
    return {
      statusCode: 500,
      headers: corsHeaders,
      body: JSON.stringify({ error: error.message }),
    };
  }
}

function serveLaunchPage(): ApiResponse {
  return {
    statusCode: 200,
    headers: {
      'Content-Type': 'text/html',
      'Cache-Control': 'no-cache',
    },
    body: LAUNCH_PAGE_HTML,
  };
}

async function getProfiles(): Promise<Profile[]> {
  try {
    const result = await ssmClient.send(
      new GetParameterCommand({ Name: PROFILES_PARAM })
    );
    return JSON.parse(result.Parameter?.Value || '[]');
  } catch (error: any) {
    if (error.name === 'ParameterNotFound') {
      return [];
    }
    throw error;
  }
}

async function getRunningTasks(): Promise<Map<string, { taskId: string; ip: string }>> {
  const taskMap = new Map<string, { taskId: string; ip: string }>();

  const listResult = await ecsClient.send(
    new ListTasksCommand({ cluster: CLUSTER })
  );

  if (!listResult.taskArns || listResult.taskArns.length === 0) {
    return taskMap;
  }

  const descResult = await ecsClient.send(
    new DescribeTasksCommand({
      cluster: CLUSTER,
      tasks: listResult.taskArns,
      include: ['TAGS'],
    })
  );

  for (const task of descResult.tasks || []) {
    const profileTag = task.tags?.find((t: Tag) => t.key === 'frank-profile');
    if (profileTag?.value) {
      const taskId = task.taskArn?.split('/').pop() || '';
      let ip = '';

      // Extract IP from attachments
      for (const att of task.attachments || []) {
        if (att.type === 'ElasticNetworkInterface') {
          const ipDetail = att.details?.find(
            (d: KeyValuePair) => d.name === 'privateIPv4Address'
          );
          if (ipDetail?.value) {
            ip = ipDetail.value;
          }
        }
      }

      taskMap.set(profileTag.value, { taskId, ip });
    }
  }

  return taskMap;
}

async function fetchActiveUsers(
  ip: string
): Promise<{ count: number; users: Array<{ display_name: string; short_id: string }> }> {
  try {
    // Fetch from container's status endpoint (port 7680)
    const controller = new AbortController();
    const timeoutId = setTimeout(() => controller.abort(), 3000); // 3s timeout

    const response = await fetch(`http://${ip}:7680/status/users`, {
      signal: controller.signal,
    });
    clearTimeout(timeoutId);

    if (response.ok) {
      const data = (await response.json()) as {
        count?: number;
        users?: Array<{ display_name: string; short_id: string }>;
      };
      return {
        count: data.count || 0,
        users: data.users || [],
      };
    }
  } catch (e) {
    // Ignore fetch errors (task may be starting up)
    console.log(`Failed to fetch active users from ${ip}:`, e);
  }
  return { count: 0, users: [] };
}

async function listProfiles(): Promise<ApiResponse> {
  const profiles = await getProfiles();
  const runningTasks = await getRunningTasks();

  // Build base statuses
  const statuses: ProfileStatus[] = await Promise.all(
    profiles.map(async (p) => {
      const running = runningTasks.get(p.name);
      const status: ProfileStatus = {
        ...p,
        status: running ? 'running' : 'stopped',
        taskId: running?.taskId,
        url: `https://${DOMAIN}/${p.name}/`,
      };

      // Fetch active users for running tasks
      if (running?.ip) {
        const userInfo = await fetchActiveUsers(running.ip);
        status.activeUsers = userInfo.count;
        status.users = userInfo.users;
      }

      return status;
    })
  );

  return {
    statusCode: 200,
    headers: corsHeaders,
    body: JSON.stringify({ profiles: statuses }),
  };
}

async function getInfrastructure(): Promise<{
  vpcId: string;
  listenerArn: string;
}> {
  // Find ALB
  const albResult = await elbClient.send(
    new DescribeLoadBalancersCommand({ Names: [ALB_NAME] })
  );
  const alb = albResult.LoadBalancers?.[0];
  if (!alb) {
    throw new Error('ALB not found');
  }

  // Find HTTPS listener
  const listenerResult = await elbClient.send(
    new DescribeListenersCommand({ LoadBalancerArn: alb.LoadBalancerArn })
  );
  const httpsListener = listenerResult.Listeners?.find((l: Listener) => l.Port === 443);
  if (!httpsListener) {
    throw new Error('HTTPS listener not found');
  }

  return {
    vpcId: alb.VpcId || '',
    listenerArn: httpsListener.ListenerArn || '',
  };
}

// Port definitions for profile routing
const PORTS = {
  wrapper: 7680,  // HTML wrapper with context panel
  claude: 7681,   // Claude terminal (ttyd)
  bash: 7682,     // Bash terminal (ttyd)
  health: 7683,   // Health check endpoint
};

// Target group suffixes and their ports
const TARGET_GROUP_CONFIGS = [
  { suffix: '', port: PORTS.wrapper, pathSuffix: '' },      // Main wrapper
  { suffix: '-t', port: PORTS.claude, pathSuffix: '/_t' },  // Claude terminal
  { suffix: '-b', port: PORTS.bash, pathSuffix: '/_b' },    // Bash terminal
];

/**
 * Delete a target group and any listener rules that reference it.
 * This is needed when a target group has the wrong port (ports can't be modified).
 */
async function deleteTargetGroup(targetGroupArn: string): Promise<void> {
  // Find and delete any listener rules that use this target group
  const infra = await getInfrastructure();
  const rulesResult = await elbClient.send(
    new DescribeRulesCommand({ ListenerArn: infra.listenerArn })
  );

  for (const rule of rulesResult.Rules || []) {
    // Skip the default rule (it can't be deleted)
    if (rule.IsDefault) continue;

    // Check if this rule forwards to our target group
    const usesTargetGroup = rule.Actions?.some(
      (action) => action.TargetGroupArn === targetGroupArn
    );

    if (usesTargetGroup && rule.RuleArn) {
      console.log(`Deleting listener rule ${rule.RuleArn} that references target group`);
      await elbClient.send(new DeleteRuleCommand({ RuleArn: rule.RuleArn }));
    }
  }

  // Now delete the target group
  console.log(`Deleting target group ${targetGroupArn}`);
  await elbClient.send(new DeleteTargetGroupCommand({ TargetGroupArn: targetGroupArn }));
}

async function ensureTargetGroupWithPort(
  profileName: string,
  vpcId: string,
  suffix: string,
  port: number
): Promise<string> {
  const tgName = `frank-profile-${profileName}${suffix}`.substring(0, 32);

  // Check if exists
  try {
    const existing = await elbClient.send(
      new DescribeTargetGroupsCommand({ Names: [tgName] })
    );
    if (existing.TargetGroups?.[0]) {
      const existingTg = existing.TargetGroups[0];
      const existingPort = existingTg.Port;

      // If port matches, reuse the target group
      if (existingPort === port) {
        return existingTg.TargetGroupArn || '';
      }

      // Port mismatch - need to delete and recreate
      // Target groups can't have their port modified, so we must recreate
      console.log(`Target group ${tgName} has wrong port ${existingPort}, expected ${port}. Deleting and recreating.`);

      // First, we need to delete any listener rules that reference this target group
      // and deregister any targets, then delete the target group
      await deleteTargetGroup(existingTg.TargetGroupArn || '');
    }
  } catch (error: any) {
    if (error.name !== 'TargetGroupNotFoundException') {
      throw error;
    }
  }

  // Create new target group
  const result = await elbClient.send(
    new CreateTargetGroupCommand({
      Name: tgName,
      Protocol: 'HTTP',
      Port: port,
      VpcId: vpcId,
      TargetType: 'ip',
      HealthCheckEnabled: true,
      HealthCheckPath: '/health',
      HealthCheckPort: String(PORTS.health),
      HealthCheckProtocol: 'HTTP',
      HealthCheckIntervalSeconds: 30,
      HealthCheckTimeoutSeconds: 10,
      HealthyThresholdCount: 2,
      UnhealthyThresholdCount: 3,
      Matcher: { HttpCode: '200' },
      Tags: [{ Key: 'frank-profile', Value: profileName }],
    })
  );

  return result.TargetGroups?.[0]?.TargetGroupArn || '';
}

interface TargetGroupInfo {
  arn: string;
  port: number;
  pathSuffix: string;
}

async function ensureAllTargetGroups(
  profileName: string,
  vpcId: string
): Promise<TargetGroupInfo[]> {
  const results: TargetGroupInfo[] = [];

  for (const config of TARGET_GROUP_CONFIGS) {
    const arn = await ensureTargetGroupWithPort(
      profileName,
      vpcId,
      config.suffix,
      config.port
    );
    results.push({
      arn,
      port: config.port,
      pathSuffix: config.pathSuffix,
    });
  }

  return results;
}

async function ensureListenerRuleWithPriority(
  listenerArn: string,
  profileName: string,
  targetGroupArn: string,
  pathPatterns: string[],
  priority: number,
  skipAuth: boolean = false
): Promise<void> {
  // Check if rule exists
  const rulesResult = await elbClient.send(
    new DescribeRulesCommand({ ListenerArn: listenerArn })
  );

  for (const rule of rulesResult.Rules || []) {
    for (const cond of rule.Conditions || []) {
      if (cond.PathPatternConfig?.Values?.includes(pathPatterns[0])) {
        // Rule exists - check if priority is acceptable (within 5 of target)
        const existingPriority = parseInt(rule.Priority || '0');
        if (Math.abs(existingPriority - priority) <= 5) {
          return; // Rule exists at acceptable priority
        }
        // Rule exists but at wrong priority (could be shadowed by catch-all)
        // Delete and recreate at correct priority
        console.log(
          `Rule for ${pathPatterns[0]} exists at priority ${existingPriority} but expected ~${priority}, recreating`
        );
        await elbClient.send(new DeleteRuleCommand({ RuleArn: rule.RuleArn! }));
        break; // Fall through to create rule at correct priority
      }
    }
  }

  // Build actions array - include Cognito auth if configured (unless skipAuth)
  const actions: any[] = [];

  if (!skipAuth && COGNITO_USER_POOL_ARN && COGNITO_CLIENT_ID && COGNITO_DOMAIN) {
    // Add Cognito authentication action first (Order: 1)
    actions.push({
      Type: 'authenticate-cognito',
      Order: 1,
      AuthenticateCognitoConfig: {
        UserPoolArn: COGNITO_USER_POOL_ARN,
        UserPoolClientId: COGNITO_CLIENT_ID,
        UserPoolDomain: COGNITO_DOMAIN,
        SessionCookieName: 'AWSELBAuthSessionCookie',
        Scope: 'openid',
        SessionTimeout: 604800, // 7 days
        OnUnauthenticatedRequest: 'authenticate',
      },
    });
    // Forward action comes after auth (Order: 2)
    actions.push({
      Type: 'forward',
      Order: 2,
      TargetGroupArn: targetGroupArn,
    });
  } else {
    // No Cognito config or auth skipped - just forward (no auth)
    if (!skipAuth) {
      console.warn('Cognito not configured - creating rule without authentication');
    }
    actions.push({
      Type: 'forward',
      TargetGroupArn: targetGroupArn,
    });
  }

  // Create rule with path-based routing and authentication
  try {
    await elbClient.send(
      new CreateRuleCommand({
        ListenerArn: listenerArn,
        Priority: priority,
        Conditions: [
          {
            Field: 'path-pattern',
            PathPatternConfig: { Values: pathPatterns },
          },
        ],
        Actions: actions,
        Tags: [{ Key: 'frank-profile', Value: profileName }],
      })
    );
  } catch (error: any) {
    // Handle priority conflict by trying nearby priorities
    // Try lower numbers first (higher precedence) to avoid being shadowed by catch-all rules
    if (error.name === 'PriorityInUseException') {
      const offsets = [-1, -2, -3, 1, 2, 3, 4, 5];
      for (const offset of offsets) {
        const tryPriority = priority + offset;
        if (tryPriority < 1) continue; // ALB priorities must be >= 1
        try {
          await elbClient.send(
            new CreateRuleCommand({
              ListenerArn: listenerArn,
              Priority: tryPriority,
              Conditions: [
                {
                  Field: 'path-pattern',
                  PathPatternConfig: { Values: pathPatterns },
                },
              ],
              Actions: actions,
              Tags: [{ Key: 'frank-profile', Value: profileName }],
            })
          );
          return;
        } catch (retryError: any) {
          if (retryError.name !== 'PriorityInUseException') {
            throw retryError;
          }
        }
      }
    }
    throw error;
  }
}

async function ensureAllListenerRules(
  listenerArn: string,
  profileName: string,
  targetGroups: TargetGroupInfo[]
): Promise<void> {
  // Calculate base priority from hash (100-800 range to leave room for 4 rules)
  const basePriority = 100 + (hashCode(profileName) % 696);

  // Find the wrapper target group (port 7680) for the no-auth status rule
  const wrapperTg = targetGroups.find((tg) => tg.pathSuffix === '');

  // Create rules in order of specificity (most specific first = lower priority number)
  // Priority order: status (no auth) < _t (Claude terminal) < _b (Bash terminal) < wrapper (catch-all)
  // The catch-all MUST be created last so its priority number is highest (lowest precedence)

  // Find the wrapper target group (port 7680) for the no-auth status rule
  if (wrapperTg) {
    await ensureListenerRuleWithPriority(
      listenerArn,
      profileName,
      wrapperTg.arn,
      [`/${profileName}/status`, `/${profileName}/status/*`],
      basePriority + 0, // Highest priority (lowest number)
      true // skipAuth
    );
  }

  for (let i = 0; i < targetGroups.length; i++) {
    const tg = targetGroups[i];
    let pathPatterns: string[];
    let priorityOffset: number;

    if (tg.pathSuffix === '/_t') {
      pathPatterns = [`/${profileName}/_t`, `/${profileName}/_t/*`];
      priorityOffset = 1;
    } else if (tg.pathSuffix === '/_b') {
      pathPatterns = [`/${profileName}/_b`, `/${profileName}/_b/*`];
      priorityOffset = 2;
    } else {
      pathPatterns = [`/${profileName}`, `/${profileName}/*`];
      priorityOffset = 3;
    }

    await ensureListenerRuleWithPriority(
      listenerArn,
      profileName,
      tg.arn,
      pathPatterns,
      basePriority + priorityOffset
    );
  }
}

function hashCode(str: string): number {
  let hash = 0;
  for (let i = 0; i < str.length; i++) {
    const char = str.charCodeAt(i);
    hash = (hash << 5) - hash + char;
    hash = hash & hash; // Convert to 32bit integer
  }
  return Math.abs(hash);
}

async function startProfile(profileName: string): Promise<ApiResponse> {
  const profiles = await getProfiles();
  const profile = profiles.find((p) => p.name === profileName);

  if (!profile) {
    return {
      statusCode: 404,
      headers: corsHeaders,
      body: JSON.stringify({ error: `Profile "${profileName}" not found` }),
    };
  }

  // Check if already running
  const runningTasks = await getRunningTasks();
  if (runningTasks.has(profileName)) {
    const task = runningTasks.get(profileName)!;
    return {
      statusCode: 200,
      headers: corsHeaders,
      body: JSON.stringify({
        message: 'Profile already running',
        taskId: task.taskId,
        url: `https://${DOMAIN}/${profileName}/`,
      }),
    };
  }

  // Get infrastructure
  const infra = await getInfrastructure();

  // Ensure ALB infrastructure (3 target groups and 3 listener rules per profile)
  const targetGroups = await ensureAllTargetGroups(profileName, infra.vpcId);
  await ensureAllListenerRules(infra.listenerArn, profileName, targetGroups);

  // Get task definition from service
  const descServices = await ecsClient.send(
    new DescribeServicesCommand({
      cluster: CLUSTER,
      services: [SERVICE],
    })
  );

  const service = descServices.services?.[0];
  if (!service) {
    throw new Error('Service not found');
  }

  // Run task
  const runResult = await ecsClient.send(
    new RunTaskCommand({
      cluster: CLUSTER,
      taskDefinition: service.taskDefinition,
      launchType: 'FARGATE',
      networkConfiguration: service.networkConfiguration,
      enableExecuteCommand: true,
      overrides: {
        containerOverrides: [
          {
            name: 'frank',
            environment: [
              { name: 'CONTAINER_NAME', value: profileName },
              { name: 'GIT_REPO', value: profile.repo },
              { name: 'GIT_BRANCH', value: profile.branch || 'main' },
              { name: 'URL_PREFIX', value: `/${profileName}` },
            ],
          },
        ],
      },
      tags: [{ key: 'frank-profile', value: profileName }],
    })
  );

  const task = runResult.tasks?.[0];
  if (!task) {
    const failure = runResult.failures?.[0];
    throw new Error(
      `Failed to start task: ${failure?.reason} - ${failure?.detail}`
    );
  }

  const taskId = task.taskArn?.split('/').pop() || '';
  const taskArn = task.taskArn || '';

  // Poll for task IP and register with target group
  // Wait up to 60 seconds for task to get an IP
  let taskIp = '';
  for (let i = 0; i < 12; i++) {
    await sleep(5000);

    const descResult = await ecsClient.send(
      new DescribeTasksCommand({
        cluster: CLUSTER,
        tasks: [taskArn],
      })
    );

    const taskInfo = descResult.tasks?.[0];
    if (taskInfo?.lastStatus === 'STOPPED') {
      throw new Error('Task stopped unexpectedly');
    }

    // Extract IP from attachments
    for (const att of taskInfo?.attachments || []) {
      if (att.type === 'ElasticNetworkInterface') {
        const ipDetail = att.details?.find(
          (d: KeyValuePair) => d.name === 'privateIPv4Address'
        );
        if (ipDetail?.value) {
          taskIp = ipDetail.value;
          break;
        }
      }
    }

    if (taskIp) break;
  }

  if (taskIp) {
    // Register task IP with all three target groups (wrapper, claude terminal, bash terminal)
    for (const tg of targetGroups) {
      await elbClient.send(
        new RegisterTargetsCommand({
          TargetGroupArn: tg.arn,
          Targets: [{ Id: taskIp, Port: tg.port }],
        })
      );
      console.log(`Registered target ${taskIp}:${tg.port} with target group`);
    }
  } else {
    console.warn('Could not get task IP within timeout, targets not registered');
  }

  return {
    statusCode: 200,
    headers: corsHeaders,
    body: JSON.stringify({
      message: taskIp ? 'Profile started' : 'Profile starting (target registration pending)',
      taskId,
      taskIp,
      url: `https://${DOMAIN}/${profileName}/`,
    }),
  };
}

function sleep(ms: number): Promise<void> {
  return new Promise(resolve => setTimeout(resolve, ms));
}

async function stopProfile(profileName: string): Promise<ApiResponse> {
  const runningTasks = await getRunningTasks();
  const task = runningTasks.get(profileName);

  if (!task) {
    return {
      statusCode: 404,
      headers: corsHeaders,
      body: JSON.stringify({
        error: `No running task found for profile "${profileName}"`,
      }),
    };
  }

  // Deregister from all three target groups (wrapper, claude terminal, bash terminal)
  for (const config of TARGET_GROUP_CONFIGS) {
    try {
      const tgName = `frank-profile-${profileName}${config.suffix}`.substring(0, 32);
      const tgResult = await elbClient.send(
        new DescribeTargetGroupsCommand({ Names: [tgName] })
      );
      const tgArn = tgResult.TargetGroups?.[0]?.TargetGroupArn;
      if (tgArn && task.ip) {
        await elbClient.send(
          new DeregisterTargetsCommand({
            TargetGroupArn: tgArn,
            Targets: [{ Id: task.ip, Port: config.port }],
          })
        );
        console.log(`Deregistered target ${task.ip}:${config.port} from ${tgName}`);
      }
    } catch (error) {
      console.warn(`Failed to deregister target from ${config.suffix || 'main'} target group:`, error);
    }
  }

  // Stop task
  await ecsClient.send(
    new StopTaskCommand({
      cluster: CLUSTER,
      task: task.taskId,
      reason: 'Stopped via Frank API',
    })
  );

  return {
    statusCode: 200,
    headers: corsHeaders,
    body: JSON.stringify({
      message: 'Profile stopped',
      taskId: task.taskId,
    }),
  };
}

// Inline HTML for the launch page
const LAUNCH_PAGE_HTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Frank - Claude Code on AWS</title>
  <style>
    :root {
      --bg-primary: #0d1117;
      --bg-secondary: #161b22;
      --bg-tertiary: #21262d;
      --text-primary: #e6edf3;
      --text-secondary: #8b949e;
      --accent: #58a6ff;
      --success: #3fb950;
      --warning: #d29922;
      --danger: #f85149;
      --border: #30363d;
    }
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'Noto Sans', Helvetica, Arial, sans-serif;
      background: var(--bg-primary);
      color: var(--text-primary);
      min-height: 100vh;
      padding: 2rem;
    }
    .container { max-width: 1100px; margin: 0 auto; }
    header { text-align: center; margin-bottom: 2rem; }
    h1 {
      font-size: 2.5rem;
      font-weight: 600;
      margin-bottom: 0.5rem;
      background: linear-gradient(135deg, var(--accent), #a371f7);
      -webkit-background-clip: text;
      -webkit-text-fill-color: transparent;
      background-clip: text;
    }
    .subtitle { color: var(--text-secondary); font-size: 1.1rem; }

    /* Toolbar: search + category filters */
    .toolbar {
      display: flex;
      gap: 1rem;
      align-items: center;
      margin-bottom: 1rem;
      flex-wrap: wrap;
    }
    .search-box {
      flex: 1;
      min-width: 200px;
      padding: 0.5rem 0.75rem;
      background: var(--bg-secondary);
      border: 1px solid var(--border);
      border-radius: 6px;
      color: var(--text-primary);
      font-size: 0.9rem;
      outline: none;
      transition: border-color 0.2s;
    }
    .search-box::placeholder { color: var(--text-secondary); }
    .search-box:focus { border-color: var(--accent); }
    .category-filters {
      display: flex;
      gap: 0.25rem;
      flex-wrap: wrap;
    }
    .cat-btn {
      padding: 0.35rem 0.75rem;
      border-radius: 20px;
      border: 1px solid var(--border);
      background: transparent;
      color: var(--text-secondary);
      font-size: 0.8rem;
      cursor: pointer;
      transition: all 0.15s;
    }
    .cat-btn:hover { border-color: var(--accent); color: var(--text-primary); }
    .cat-btn.active { background: var(--accent); border-color: var(--accent); color: #fff; }
    .cat-count {
      display: inline-block;
      background: var(--bg-tertiary);
      border-radius: 10px;
      padding: 0 0.4rem;
      font-size: 0.75rem;
      margin-left: 0.25rem;
    }
    .cat-btn.active .cat-count { background: rgba(255,255,255,0.2); }

    /* Table */
    .profiles-table {
      width: 100%;
      border-collapse: collapse;
      background: var(--bg-secondary);
      border: 1px solid var(--border);
      border-radius: 8px;
      overflow: hidden;
    }
    .profiles-table th {
      text-align: left;
      padding: 0.75rem 1rem;
      background: var(--bg-tertiary);
      color: var(--text-secondary);
      font-size: 0.8rem;
      font-weight: 600;
      text-transform: uppercase;
      letter-spacing: 0.05em;
      border-bottom: 1px solid var(--border);
      cursor: pointer;
      user-select: none;
      white-space: nowrap;
    }
    .profiles-table th:hover { color: var(--text-primary); }
    .profiles-table th .sort-arrow { margin-left: 0.25rem; font-size: 0.7rem; }
    .profiles-table td {
      padding: 0.65rem 1rem;
      border-bottom: 1px solid var(--border);
      font-size: 0.9rem;
      vertical-align: middle;
    }
    .profiles-table tr:last-child td { border-bottom: none; }
    .profiles-table tbody tr { transition: background 0.1s; }
    .profiles-table tbody tr:hover { background: var(--bg-tertiary); }
    .category-header td {
      background: var(--bg-primary);
      padding: 0.5rem 1rem;
      font-weight: 600;
      font-size: 0.8rem;
      color: var(--text-secondary);
      text-transform: uppercase;
      letter-spacing: 0.05em;
      border-bottom: 1px solid var(--border);
    }
    .profile-name { font-weight: 600; color: var(--text-primary); }
    .profile-desc { color: var(--text-secondary); font-size: 0.85rem; }
    .profile-repo {
      font-family: 'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, monospace;
      font-size: 0.8rem;
      color: var(--text-secondary);
    }
    .profile-branch {
      font-family: 'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, monospace;
      font-size: 0.8rem;
      background: var(--bg-tertiary);
      padding: 0.15rem 0.4rem;
      border-radius: 4px;
      color: var(--text-secondary);
    }
    .status-badge {
      padding: 0.2rem 0.6rem;
      border-radius: 20px;
      font-size: 0.8rem;
      font-weight: 500;
      text-transform: uppercase;
      display: inline-block;
    }
    .status-running { background: rgba(63, 185, 80, 0.15); color: var(--success); }
    .status-stopped { background: rgba(139, 148, 158, 0.15); color: var(--text-secondary); }
    .status-starting { background: rgba(210, 153, 34, 0.15); color: var(--warning); }
    .users-badge {
      padding: 0.2rem 0.5rem;
      border-radius: 12px;
      font-size: 0.75rem;
      font-weight: 500;
      display: inline-flex;
      align-items: center;
      gap: 4px;
    }
    .users-badge.has-users { background: rgba(63, 185, 80, 0.15); color: var(--success); }
    .users-badge.no-users { background: var(--bg-tertiary); color: var(--text-secondary); }
    .url-link {
      font-family: 'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, monospace;
      font-size: 0.8rem;
      color: var(--accent);
      text-decoration: none;
    }
    .url-link:hover { text-decoration: underline; }
    .actions-cell { white-space: nowrap; }
    .actions-cell a, .actions-cell button { margin-right: 0.5rem; }
    button {
      padding: 0.35rem 0.75rem;
      border-radius: 6px;
      border: 1px solid var(--border);
      font-size: 0.8rem;
      font-weight: 500;
      cursor: pointer;
      transition: all 0.2s;
    }
    .btn-start { background: var(--success); border-color: var(--success); color: #fff; }
    .btn-start:hover { background: #2ea043; }
    .btn-stop { background: transparent; border-color: var(--danger); color: var(--danger); }
    .btn-stop:hover { background: var(--danger); color: #fff; }
    .btn-open { background: var(--accent); border-color: var(--accent); color: #fff; text-decoration: none; display: inline-block; padding: 0.35rem 0.75rem; border-radius: 6px; font-size: 0.8rem; font-weight: 500; }
    .btn-open:hover { background: #4393e6; }
    button:disabled { opacity: 0.5; cursor: not-allowed; }
    .loading { text-align: center; padding: 3rem; color: var(--text-secondary); }
    .spinner {
      display: inline-block; width: 24px; height: 24px;
      border: 2px solid var(--border); border-top-color: var(--accent);
      border-radius: 50%; animation: spin 1s linear infinite;
      margin-right: 0.5rem; vertical-align: middle;
    }
    @keyframes spin { to { transform: rotate(360deg); } }
    .error {
      background: rgba(248, 81, 73, 0.1);
      border: 1px solid var(--danger);
      color: var(--danger);
      padding: 1rem; border-radius: 8px; margin-bottom: 1rem;
    }
    .empty-state {
      text-align: center; padding: 4rem 2rem;
      background: var(--bg-secondary); border-radius: 8px; border: 1px dashed var(--border);
    }
    .empty-state h3 { margin-bottom: 0.5rem; }
    .empty-state p { color: var(--text-secondary); }
    .empty-state code {
      display: block; margin-top: 1rem; padding: 1rem;
      background: var(--bg-tertiary); border-radius: 6px; font-family: monospace;
    }
    .no-results {
      text-align: center; padding: 2rem; color: var(--text-secondary);
    }
    footer {
      text-align: center; margin-top: 3rem; padding-top: 2rem;
      border-top: 1px solid var(--border); color: var(--text-secondary); font-size: 0.9rem;
    }
    footer a { color: var(--accent); text-decoration: none; }
    footer a:hover { text-decoration: underline; }
  </style>
</head>
<body>
  <div class="container">
    <header>
      <h1>Frank</h1>
      <p class="subtitle">Claude Code on AWS ECS</p>
      <nav style="margin-top: 1rem;">
        <a href="/dashboard" style="color: var(--accent); text-decoration: none; padding: 0.5rem 1rem; border: 1px solid var(--accent); border-radius: 6px; font-size: 0.9rem;">Analytics Dashboard</a>
      </nav>
    </header>
    <div id="error" class="error" style="display: none;"></div>
    <div id="loading" class="loading"><span class="spinner"></span> Loading profiles...</div>
    <div id="content" style="display: none;">
      <div class="toolbar">
        <input type="text" id="search" class="search-box" placeholder="Search profiles..." oninput="renderProfiles()">
        <div id="categories" class="category-filters"></div>
      </div>
      <div id="table-wrap"></div>
    </div>
    <div id="empty" class="empty-state" style="display: none;">
      <h3>No profiles configured</h3>
      <p>Add a profile using the Frank CLI:</p>
      <code>frank profile add myproject --repo https://github.com/user/repo.git</code>
    </div>
    <footer>
      <p><a href="https://github.com/barff/autoclauto" target="_blank">GitHub</a> &middot; Powered by Claude Code</p>
    </footer>
  </div>
  <script>
    const API_BASE = '/api';
    let profiles = [];
    let activeCategory = 'all';
    let sortCol = 'name';
    let sortAsc = true;

    async function fetchProfiles() {
      try {
        document.getElementById('loading').style.display = 'block';
        document.getElementById('content').style.display = 'none';
        document.getElementById('empty').style.display = 'none';
        document.getElementById('error').style.display = 'none';
        const response = await fetch(API_BASE + '/profiles', { credentials: 'include' });
        if (!response.ok) throw new Error('Failed to fetch profiles');
        const data = await response.json();
        profiles = data.profiles || [];
        renderProfiles();
      } catch (error) {
        document.getElementById('error').textContent = error.message;
        document.getElementById('error').style.display = 'block';
        document.getElementById('loading').style.display = 'none';
      }
    }

    function getCategories() {
      const cats = {};
      for (const p of profiles) {
        const c = p.category || 'Uncategorized';
        cats[c] = (cats[c] || 0) + 1;
      }
      return cats;
    }

    function setCategory(cat) {
      activeCategory = cat;
      renderProfiles();
    }

    function setSort(col) {
      if (sortCol === col) { sortAsc = !sortAsc; }
      else { sortCol = col; sortAsc = true; }
      renderProfiles();
    }

    function renderProfiles() {
      document.getElementById('loading').style.display = 'none';
      if (profiles.length === 0) {
        document.getElementById('empty').style.display = 'block';
        return;
      }
      document.getElementById('content').style.display = 'block';

      // Build category tabs
      const cats = getCategories();
      const catKeys = Object.keys(cats).sort();
      const catsEl = document.getElementById('categories');
      catsEl.innerHTML =
        '<button class="cat-btn ' + (activeCategory === 'all' ? 'active' : '') + '" onclick="setCategory(\\'all\\')">' +
          'All<span class="cat-count">' + profiles.length + '</span></button>' +
        catKeys.map(c =>
          '<button class="cat-btn ' + (activeCategory === c ? 'active' : '') + '" onclick="setCategory(\\'' + c.replace(/'/g, "\\\\'") + '\\')">' +
            c + '<span class="cat-count">' + cats[c] + '</span></button>'
        ).join('');

      // Filter
      const query = (document.getElementById('search').value || '').toLowerCase();
      let filtered = profiles.filter(p => {
        if (activeCategory !== 'all' && (p.category || 'Uncategorized') !== activeCategory) return false;
        if (query) {
          const haystack = (p.name + ' ' + (p.description || '') + ' ' + p.repo + ' ' + (p.branch || '') + ' ' + (p.category || '')).toLowerCase();
          return haystack.includes(query);
        }
        return true;
      });

      // Sort
      filtered.sort((a, b) => {
        let va, vb;
        if (sortCol === 'status') { va = a.status; vb = b.status; }
        else if (sortCol === 'users') {
          // Sort by active users count (numeric)
          const countA = a.activeUsers || 0;
          const countB = b.activeUsers || 0;
          return sortAsc ? countA - countB : countB - countA;
        }
        else { va = a.name; vb = b.name; }
        const cmp = (va || '').localeCompare(vb || '');
        return sortAsc ? cmp : -cmp;
      });

      const wrap = document.getElementById('table-wrap');

      if (filtered.length === 0) {
        wrap.innerHTML = '<div class="no-results">No profiles match your search.</div>';
        return;
      }

      // Group by category when showing all
      const showGroups = activeCategory === 'all' && catKeys.length > 1;
      let grouped;
      if (showGroups) {
        grouped = {};
        for (const p of filtered) {
          const c = p.category || 'Uncategorized';
          if (!grouped[c]) grouped[c] = [];
          grouped[c].push(p);
        }
      }

      function arrow(col) {
        if (sortCol !== col) return '';
        return '<span class="sort-arrow">' + (sortAsc ? '&#9650;' : '&#9660;') + '</span>';
      }

      let html = '<table class="profiles-table"><thead><tr>' +
        '<th onclick="setSort(\\'name\\')">Name' + arrow('name') + '</th>' +
        '<th>Description</th>' +
        '<th onclick="setSort(\\'status\\')">Status' + arrow('status') + '</th>' +
        '<th onclick="setSort(\\'users\\')">Users' + arrow('users') + '</th>' +
        '<th>URL</th>' +
        '<th>Actions</th>' +
        '</tr></thead><tbody>';

      function profileRow(p) {
        const actions = p.status === 'running'
          ? '<a href="' + p.url + '" target="_blank" class="btn-open">Open</a>' +
            '<button class="btn-stop" onclick="stopProfile(\\'' + p.name + '\\')">Stop</button>'
          : '<button class="btn-start" onclick="startProfile(\\'' + p.name + '\\')">Start</button>';
        const userCount = p.activeUsers || 0;
        const usersClass = userCount > 0 ? 'has-users' : 'no-users';
        const usersText = userCount > 0 ? userCount + ' online' : '-';
        const urlCell = p.status === 'running'
          ? '<a href="' + p.url + '" target="_blank" class="url-link">/' + p.name + '/</a>'
          : '<span style="color: var(--text-secondary);">-</span>';
        return '<tr data-profile="' + p.name + '">' +
          '<td class="profile-name">' + p.name + '</td>' +
          '<td class="profile-desc">' + (p.description || '-') + '</td>' +
          '<td><span class="status-badge status-' + p.status + '">' + p.status + '</span></td>' +
          '<td><span class="users-badge ' + usersClass + '">' + usersText + '</span></td>' +
          '<td>' + urlCell + '</td>' +
          '<td class="actions-cell">' + actions + '</td></tr>';
      }

      if (showGroups && grouped) {
        for (const cat of Object.keys(grouped).sort()) {
          html += '<tr class="category-header"><td colspan="6">' + cat + ' (' + grouped[cat].length + ')</td></tr>';
          html += grouped[cat].map(profileRow).join('');
        }
      } else {
        html += filtered.map(profileRow).join('');
      }

      // Note: colspan stays at 6 because we replaced Repo/Branch with Users/URL

      html += '</tbody></table>';
      wrap.innerHTML = html;
    }

    async function startProfile(name) {
      const row = document.querySelector('tr[data-profile="' + name + '"]');
      if (row) {
        const badge = row.querySelector('.status-badge');
        const btn = row.querySelector('button');
        if (badge) { badge.className = 'status-badge status-starting'; badge.textContent = 'starting'; }
        if (btn) { btn.disabled = true; btn.textContent = 'Starting...'; }
      }
      try {
        const response = await fetch(API_BASE + '/profiles/' + name + '/start', { method: 'POST', credentials: 'include' });
        if (!response.ok) {
          const data = await response.json();
          throw new Error(data.error || 'Failed to start profile');
        }
        setTimeout(fetchProfiles, 3000);
      } catch (error) {
        document.getElementById('error').textContent = error.message;
        document.getElementById('error').style.display = 'block';
        fetchProfiles();
      }
    }

    async function stopProfile(name) {
      const row = document.querySelector('tr[data-profile="' + name + '"]');
      if (row) {
        const btn = row.querySelector('.btn-stop');
        if (btn) { btn.disabled = true; btn.textContent = 'Stopping...'; }
      }
      try {
        const response = await fetch(API_BASE + '/profiles/' + name + '/stop', { method: 'POST', credentials: 'include' });
        if (!response.ok) {
          const data = await response.json();
          throw new Error(data.error || 'Failed to stop profile');
        }
        fetchProfiles();
      } catch (error) {
        document.getElementById('error').textContent = error.message;
        document.getElementById('error').style.display = 'block';
        fetchProfiles();
      }
    }

    fetchProfiles();
    setInterval(fetchProfiles, 30000);
  </script>
</body>
</html>`;
