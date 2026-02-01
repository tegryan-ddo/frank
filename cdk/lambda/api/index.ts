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
}

interface ProfileStatus extends Profile {
  status: 'running' | 'stopped';
  taskId?: string;
  url?: string;
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

async function listProfiles(): Promise<ApiResponse> {
  const profiles = await getProfiles();
  const runningTasks = await getRunningTasks();

  const statuses: ProfileStatus[] = profiles.map((p) => {
    const running = runningTasks.get(p.name);
    return {
      ...p,
      status: running ? 'running' : 'stopped',
      taskId: running?.taskId,
      url: `https://${DOMAIN}/${p.name}/`,
    };
  });

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
        return; // Rule already exists
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
    .container { max-width: 900px; margin: 0 auto; }
    header { text-align: center; margin-bottom: 3rem; }
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
    .profiles-grid { display: grid; gap: 1rem; }
    .profile-card {
      background: var(--bg-secondary);
      border: 1px solid var(--border);
      border-radius: 8px;
      padding: 1.5rem;
      display: flex;
      justify-content: space-between;
      align-items: center;
      transition: border-color 0.2s;
    }
    .profile-card:hover { border-color: var(--accent); }
    .profile-info h3 { font-size: 1.25rem; font-weight: 600; margin-bottom: 0.25rem; }
    .profile-info .description { color: var(--text-secondary); font-size: 0.9rem; margin-bottom: 0.5rem; }
    .profile-info .repo {
      font-family: 'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, monospace;
      font-size: 0.85rem;
      color: var(--text-secondary);
      background: var(--bg-tertiary);
      padding: 0.25rem 0.5rem;
      border-radius: 4px;
      display: inline-block;
    }
    .profile-actions { display: flex; gap: 0.75rem; align-items: center; }
    .status-badge {
      padding: 0.25rem 0.75rem;
      border-radius: 20px;
      font-size: 0.85rem;
      font-weight: 500;
      text-transform: uppercase;
    }
    .status-running { background: rgba(63, 185, 80, 0.15); color: var(--success); }
    .status-stopped { background: rgba(139, 148, 158, 0.15); color: var(--text-secondary); }
    .status-starting { background: rgba(210, 153, 34, 0.15); color: var(--warning); }
    button {
      padding: 0.5rem 1rem;
      border-radius: 6px;
      border: 1px solid var(--border);
      font-size: 0.9rem;
      font-weight: 500;
      cursor: pointer;
      transition: all 0.2s;
    }
    .btn-start { background: var(--success); border-color: var(--success); color: #fff; }
    .btn-start:hover { background: #2ea043; }
    .btn-stop { background: transparent; border-color: var(--danger); color: var(--danger); }
    .btn-stop:hover { background: var(--danger); color: #fff; }
    .btn-open { background: var(--accent); border-color: var(--accent); color: #fff; text-decoration: none; display: inline-block; }
    .btn-open:hover { background: #4393e6; }
    button:disabled { opacity: 0.5; cursor: not-allowed; }
    .loading { text-align: center; padding: 3rem; color: var(--text-secondary); }
    .spinner {
      display: inline-block;
      width: 24px;
      height: 24px;
      border: 2px solid var(--border);
      border-top-color: var(--accent);
      border-radius: 50%;
      animation: spin 1s linear infinite;
      margin-right: 0.5rem;
      vertical-align: middle;
    }
    @keyframes spin { to { transform: rotate(360deg); } }
    .error {
      background: rgba(248, 81, 73, 0.1);
      border: 1px solid var(--danger);
      color: var(--danger);
      padding: 1rem;
      border-radius: 8px;
      margin-bottom: 1rem;
    }
    .empty-state {
      text-align: center;
      padding: 4rem 2rem;
      background: var(--bg-secondary);
      border-radius: 8px;
      border: 1px dashed var(--border);
    }
    .empty-state h3 { margin-bottom: 0.5rem; }
    .empty-state p { color: var(--text-secondary); }
    .empty-state code {
      display: block;
      margin-top: 1rem;
      padding: 1rem;
      background: var(--bg-tertiary);
      border-radius: 6px;
      font-family: monospace;
    }
    footer {
      text-align: center;
      margin-top: 3rem;
      padding-top: 2rem;
      border-top: 1px solid var(--border);
      color: var(--text-secondary);
      font-size: 0.9rem;
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
        <a href="/dashboard" class="nav-link" style="color: var(--accent); text-decoration: none; padding: 0.5rem 1rem; border: 1px solid var(--accent); border-radius: 6px; font-size: 0.9rem;">📊 Analytics Dashboard</a>
      </nav>
    </header>
    <div id="error" class="error" style="display: none;"></div>
    <div id="loading" class="loading"><span class="spinner"></span> Loading profiles...</div>
    <div id="profiles" class="profiles-grid" style="display: none;"></div>
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
    async function fetchProfiles() {
      try {
        document.getElementById('loading').style.display = 'block';
        document.getElementById('profiles').style.display = 'none';
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
    function renderProfiles() {
      document.getElementById('loading').style.display = 'none';
      if (profiles.length === 0) {
        document.getElementById('empty').style.display = 'block';
        return;
      }
      const container = document.getElementById('profiles');
      container.innerHTML = profiles.map(p => \`
        <div class="profile-card" data-profile="\${p.name}">
          <div class="profile-info">
            <h3>\${p.name}</h3>
            \${p.description ? '<p class="description">' + p.description + '</p>' : ''}
            <span class="repo">\${p.repo}</span>
          </div>
          <div class="profile-actions">
            <span class="status-badge status-\${p.status}">\${p.status}</span>
            \${p.status === 'running'
              ? '<a href="' + p.url + '" target="_blank" class="btn-open">Open</a><button class="btn-stop" onclick="stopProfile(\\'' + p.name + '\\')">Stop</button>'
              : '<button class="btn-start" onclick="startProfile(\\'' + p.name + '\\')">Start</button>'}
          </div>
        </div>
      \`).join('');
      container.style.display = 'grid';
    }
    async function startProfile(name) {
      const card = document.querySelector('[data-profile="' + name + '"]');
      const actions = card.querySelector('.profile-actions');
      const badge = actions.querySelector('.status-badge');
      const btn = actions.querySelector('button');
      badge.className = 'status-badge status-starting';
      badge.textContent = 'starting';
      btn.disabled = true;
      btn.textContent = 'Starting...';
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
      const card = document.querySelector('[data-profile="' + name + '"]');
      const btn = card.querySelector('.btn-stop');
      btn.disabled = true;
      btn.textContent = 'Stopping...';
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
