import * as cdk from 'aws-cdk-lib';
import * as ec2 from 'aws-cdk-lib/aws-ec2';
import * as ecs from 'aws-cdk-lib/aws-ecs';

import * as elbv2 from 'aws-cdk-lib/aws-elasticloadbalancingv2';
import * as elbv2Actions from 'aws-cdk-lib/aws-elasticloadbalancingv2-actions';
import * as elbv2Targets from 'aws-cdk-lib/aws-elasticloadbalancingv2-targets';
import * as logs from 'aws-cdk-lib/aws-logs';
import * as route53 from 'aws-cdk-lib/aws-route53';
import * as route53Targets from 'aws-cdk-lib/aws-route53-targets';
import * as acm from 'aws-cdk-lib/aws-certificatemanager';
import * as cognito from 'aws-cdk-lib/aws-cognito';
import * as secretsmanager from 'aws-cdk-lib/aws-secretsmanager';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as lambdaNodejs from 'aws-cdk-lib/aws-lambda-nodejs';
import * as ssm from 'aws-cdk-lib/aws-ssm';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as s3 from 'aws-cdk-lib/aws-s3';
import * as events from 'aws-cdk-lib/aws-events';
import * as eventsTargets from 'aws-cdk-lib/aws-events-targets';
import * as path from 'path';
import { Construct } from 'constructs';

export interface FrankStackProps extends cdk.StackProps {
  domainName: string;
  hostedZoneId: string;
  certificateArn: string;
  cognitoUserPoolId?: string;
  cognitoClientId?: string;
  cognitoDomain?: string;
}

export class FrankStack extends cdk.Stack {
  constructor(scope: Construct, id: string, props: FrankStackProps) {
    super(scope, id, props);

    // VPC - create a new one for isolation
    const vpc = new ec2.Vpc(this, 'FrankVpc', {
      maxAzs: 2,
      natGateways: 1,
      subnetConfiguration: [
        {
          name: 'Public',
          subnetType: ec2.SubnetType.PUBLIC,
          cidrMask: 24,
        },
        {
          name: 'Private',
          subnetType: ec2.SubnetType.PRIVATE_WITH_EGRESS,
          cidrMask: 24,
        },
      ],
    });

    // ECS Cluster
    const cluster = new ecs.Cluster(this, 'FrankCluster', {
      vpc,
      clusterName: 'frank',
      containerInsights: true,
    });

    // Secrets - stored in Secrets Manager
    const githubTokenSecret = new secretsmanager.Secret(this, 'GitHubToken', {
      secretName: '/frank/github-token',
      description: 'GitHub token for Frank containers',
    });

    const claudeCredentialsSecret = new secretsmanager.Secret(this, 'ClaudeCredentials', {
      secretName: '/frank/claude-credentials',
      description: 'Claude OAuth credentials for Frank containers',
    });

    const pnyxApiKeySecret = new secretsmanager.Secret(this, 'PnyxApiKey', {
      secretName: '/frank/pnyx-api-key',
      description: 'Pnyx API key for agent deliberation platform',
    });

    const openaiApiKeySecret = new secretsmanager.Secret(this, 'OpenAiApiKey', {
      secretName: '/frank/openai-api-key',
      description: 'OpenAI API key for Codex worker containers',
    });

    // =========================================================================
    // Analytics S3 Bucket
    // =========================================================================
    const analyticsBucket = new s3.Bucket(this, 'AnalyticsBucket', {
      bucketName: `frank-analytics-${this.account}`,
      removalPolicy: cdk.RemovalPolicy.RETAIN,
      encryption: s3.BucketEncryption.S3_MANAGED,
      blockPublicAccess: s3.BlockPublicAccess.BLOCK_ALL,
      lifecycleRules: [
        {
          id: 'archive-old-prompts',
          prefix: 'prompts/',
          transitions: [
            {
              storageClass: s3.StorageClass.INFREQUENT_ACCESS,
              transitionAfter: cdk.Duration.days(90),
            },
          ],
          expiration: cdk.Duration.days(365),
        },
        {
          id: 'archive-old-feedback',
          prefix: 'feedback/',
          transitions: [
            {
              storageClass: s3.StorageClass.INFREQUENT_ACCESS,
              transitionAfter: cdk.Duration.days(90),
            },
          ],
          expiration: cdk.Duration.days(365),
        },
        {
          id: 'expire-aggregates',
          prefix: 'aggregates/',
          expiration: cdk.Duration.days(730), // 2 years for aggregates
        },
      ],
    });

    // Task Definition
    const taskDefinition = new ecs.FargateTaskDefinition(this, 'FrankTask', {
      memoryLimitMiB: 8192,
      cpu: 4096,
      ephemeralStorageGiB: 50,
      runtimePlatform: {
        cpuArchitecture: ecs.CpuArchitecture.X86_64,
        operatingSystemFamily: ecs.OperatingSystemFamily.LINUX,
      },
    });

    // Grant S3 analytics bucket write access to task role
    analyticsBucket.grantWrite(taskDefinition.taskRole);

    // Grant credential sync write access to Claude credentials secret
    claudeCredentialsSecret.grantWrite(taskDefinition.taskRole);

    // Grant CodePipeline access to task role
    taskDefinition.taskRole.addToPrincipalPolicy(new iam.PolicyStatement({
      actions: [
        'codepipeline:GetPipeline',
        'codepipeline:GetPipelineState',
        'codepipeline:GetPipelineExecution',
        'codepipeline:ListPipelines',
        'codepipeline:ListPipelineExecutions',
        'codepipeline:ListActionExecutions',
        'codepipeline:RetryStageExecution',
      ],
      resources: ['*'],
    }));

    // Grant CodeBuild update access for managing build projects
    taskDefinition.taskRole.addToPrincipalPolicy(new iam.PolicyStatement({
      actions: [
        'codebuild:UpdateProject',
        'codebuild:BatchGetProjects',
      ],
      resources: [`arn:aws:codebuild:${this.region}:${this.account}:project/pnyx-*`],
    }));

    // Grant scoped IAM policy management for pnyx roles
    taskDefinition.taskRole.addToPrincipalPolicy(new iam.PolicyStatement({
      actions: [
        'iam:PutRolePolicy',
        'iam:GetRolePolicy',
      ],
      resources: [`arn:aws:iam::${this.account}:role/pnyx-*`],
    }));

    // Grant iam:PassRole for pnyx ECS task definition registration
    taskDefinition.taskRole.addToPrincipalPolicy(new iam.PolicyStatement({
      actions: ['iam:PassRole'],
      resources: [
        `arn:aws:iam::${this.account}:role/pnyx-dev-ecs-exec`,
        `arn:aws:iam::${this.account}:role/pnyx-dev-ecs-task`,
      ],
    }));

    // Grant ECS task definition permissions (inspect and update task definitions)
    taskDefinition.taskRole.addToPrincipalPolicy(new iam.PolicyStatement({
      actions: [
        'ecs:DescribeTaskDefinition',
        'ecs:RegisterTaskDefinition',
        'ecs:ListTaskDefinitions',
      ],
      resources: ['*'],
    }));

    // Grant ECS service management permissions (update services, inspect tasks)
    taskDefinition.taskRole.addToPrincipalPolicy(new iam.PolicyStatement({
      actions: [
        'ecs:UpdateService',
        'ecs:DescribeServices',
        'ecs:ListTasks',
        'ecs:DescribeTasks',
      ],
      resources: ['*'],
    }));

    // Grant Cognito permissions (look up user pools and create app clients)
    taskDefinition.taskRole.addToPrincipalPolicy(new iam.PolicyStatement({
      actions: [
        'cognito-idp:ListUserPools',
        'cognito-idp:DescribeUserPool',
        'cognito-idp:DescribeUserPoolClient',
        'cognito-idp:ListUserPoolClients',
        'cognito-idp:CreateUserPoolClient',
      ],
      resources: ['*'],
    }));

    // Grant ELB access to task role
    taskDefinition.taskRole.addToPrincipalPolicy(new iam.PolicyStatement({
      actions: [
        'elasticloadbalancing:DescribeLoadBalancers',
        'elasticloadbalancing:DescribeListeners',
        'elasticloadbalancing:DescribeRules',
        'elasticloadbalancing:DescribeTargetGroups',
        'elasticloadbalancing:DescribeTargetHealth',
        'elasticloadbalancing:DeleteRule',
        'elasticloadbalancing:CreateRule',
        'elasticloadbalancing:AddTags',
      ],
      resources: ['*'],
    }));

    // Grant SSM write access for Pnyx
    taskDefinition.taskRole.addToPrincipalPolicy(new iam.PolicyStatement({
      actions: ['ssm:PutParameter'],
      resources: ['arn:aws:ssm:*:*:parameter/pnyx/dev/*'],
    }));

    // Grant Secrets Manager access for Pnyx
    taskDefinition.taskRole.addToPrincipalPolicy(new iam.PolicyStatement({
      actions: [
        'secretsmanager:CreateSecret',
        'secretsmanager:PutSecretValue',
      ],
      resources: ['arn:aws:secretsmanager:*:*:secret:pnyx/dev/*'],
    }));

    // Grant Cognito UpdateUserPoolClient
    taskDefinition.taskRole.addToPrincipalPolicy(new iam.PolicyStatement({
      actions: ['cognito-idp:UpdateUserPoolClient'],
      resources: ['*'],
    }));

    // Grant KMS encrypt for SSM SecureString parameters
    taskDefinition.taskRole.addToPrincipalPolicy(new iam.PolicyStatement({
      actions: ['kms:Encrypt'],
      resources: ['*'],
      conditions: {
        StringEquals: {
          'kms:ViaService': 'ssm.us-east-1.amazonaws.com',
        },
      },
    }));

    // Grant ECS service management (deploy task definition updates)
    taskDefinition.taskRole.addToPrincipalPolicy(new iam.PolicyStatement({
      actions: [
        'ecs:UpdateService',
        'ecs:DescribeServices',
        'ecs:ListServices',
        'ecs:ListTasks',
        'ecs:DescribeTasks',
        'ecs:ListClusters',
      ],
      resources: ['*'],
    }));

    // Grant RDS full management for pnyx instances
    taskDefinition.taskRole.addToPrincipalPolicy(new iam.PolicyStatement({
      actions: [
        'rds:CreateDBInstance',
        'rds:DescribeDBInstances',
        'rds:ModifyDBInstance',
        'rds:AddTagsToResource',
        'rds:ListTagsForResource',
      ],
      resources: [
        `arn:aws:rds:${this.region}:${this.account}:db:pnyx-*`,
      ],
    }));

    // Grant RDS supporting resource management (subnet groups, parameter groups)
    taskDefinition.taskRole.addToPrincipalPolicy(new iam.PolicyStatement({
      actions: [
        'rds:CreateDBSubnetGroup',
        'rds:DescribeDBSubnetGroups',
        'rds:CreateDBParameterGroup',
        'rds:DescribeDBParameterGroups',
        'rds:ModifyDBParameterGroup',
        'rds:DescribeDBEngineVersions',
        'rds:DescribeOrderableDBInstanceOptions',
      ],
      resources: ['*'],
    }));

    // Grant EC2 permissions for VPC/subnet/security group operations
    taskDefinition.taskRole.addToPrincipalPolicy(new iam.PolicyStatement({
      actions: [
        'ec2:DescribeSubnets',
        'ec2:DescribeVpcs',
        'ec2:DescribeSecurityGroups',
        'ec2:CreateSecurityGroup',
        'ec2:AuthorizeSecurityGroupIngress',
        'ec2:AuthorizeSecurityGroupEgress',
        'ec2:RevokeSecurityGroupEgress',
        'ec2:CreateTags',
        'ec2:DescribeNetworkInterfaces',
        'ec2:DescribeAvailabilityZones',
      ],
      resources: ['*'],
    }));

    // Grant Cognito user/pool management (create pools, manage users)
    taskDefinition.taskRole.addToPrincipalPolicy(new iam.PolicyStatement({
      actions: [
        'cognito-idp:CreateUserPool',
        'cognito-idp:AdminCreateUser',
        'cognito-idp:AdminSetUserPassword',
        'cognito-idp:ListUsers',
      ],
      resources: ['*'],
    }));

    // Grant SSM read access for Pnyx (verify parameter values)
    taskDefinition.taskRole.addToPrincipalPolicy(new iam.PolicyStatement({
      actions: [
        'ssm:GetParameter',
        'ssm:GetParameters',
        'ssm:DescribeParameters',
        'ssm:DeleteParameter',
      ],
      resources: ['arn:aws:ssm:*:*:parameter/pnyx/dev/*'],
    }));

    // Grant Secrets Manager read access for Pnyx (verify secret values)
    taskDefinition.taskRole.addToPrincipalPolicy(new iam.PolicyStatement({
      actions: [
        'secretsmanager:GetSecretValue',
        'secretsmanager:DescribeSecret',
        'secretsmanager:ListSecrets',
        'secretsmanager:DeleteSecret',
      ],
      resources: ['arn:aws:secretsmanager:*:*:secret:pnyx/dev/*'],
    }));

    // Grant CodePipeline start (trigger deployments)
    taskDefinition.taskRole.addToPrincipalPolicy(new iam.PolicyStatement({
      actions: ['codepipeline:StartPipelineExecution'],
      resources: [`arn:aws:codepipeline:${this.region}:${this.account}:pnyx-*`],
    }));

    // Grant CloudWatch Logs read access (debug ECS tasks)
    taskDefinition.taskRole.addToPrincipalPolicy(new iam.PolicyStatement({
      actions: [
        'logs:GetLogEvents',
        'logs:FilterLogEvents',
        'logs:DescribeLogStreams',
        'logs:DescribeLogGroups',
      ],
      resources: [`arn:aws:logs:${this.region}:${this.account}:log-group:/ecs/pnyx-*:*`],
    }));

    // Log group
    const logGroup = new logs.LogGroup(this, 'FrankLogs', {
      logGroupName: '/ecs/frank',
      retention: logs.RetentionDays.ONE_WEEK,
      removalPolicy: cdk.RemovalPolicy.DESTROY,
    });

    // Container Definition
    const container = taskDefinition.addContainer('frank', {
      image: ecs.ContainerImage.fromAsset('..', {
        file: 'build/Dockerfile.ecs',
        exclude: ['cdk', 'cdk.out', 'node_modules', '.git'],
      }),
      logging: ecs.LogDrivers.awsLogs({
        logGroup,
        streamPrefix: 'frank',
      }),
      environment: {
        WEB_PORT: '7680',
        TTYD_PORT: '7681',
        BASH_PORT: '7682',
        STATUS_PORT: '7683',
        AWS_REGION: this.region,
        ANALYTICS_BUCKET: analyticsBucket.bucketName,
        ANALYTICS_ENABLED: 'true',
        // Clone this repo on container startup
        GIT_REPO: 'https://github.com/tegryan-ddo/enkai.git',
        GIT_BRANCH: 'main',
      },
      secrets: {
        GITHUB_TOKEN: ecs.Secret.fromSecretsManager(githubTokenSecret),
        CLAUDE_CREDENTIALS: ecs.Secret.fromSecretsManager(claudeCredentialsSecret),
        PNYX_API_KEY: ecs.Secret.fromSecretsManager(pnyxApiKeySecret),
      },
      portMappings: [
        { containerPort: 7680, name: 'web' },
        { containerPort: 7681, name: 'claude' },
        { containerPort: 7682, name: 'bash' },
        { containerPort: 7683, name: 'status' },
      ],
      healthCheck: {
        command: ['CMD-SHELL', 'curl -f http://localhost:7683/health || exit 1'],
        interval: cdk.Duration.seconds(30),
        timeout: cdk.Duration.seconds(10),
        retries: 3,
        startPeriod: cdk.Duration.seconds(60),
      },
    });

    // Import certificate
    const certificate = acm.Certificate.fromCertificateArn(
      this,
      'Certificate',
      props.certificateArn
    );

    // ALB
    const alb = new elbv2.ApplicationLoadBalancer(this, 'FrankAlb', {
      vpc,
      internetFacing: true,
      loadBalancerName: 'frank-alb',
      idleTimeout: cdk.Duration.seconds(3600), // 1 hour timeout for long-running WebSocket connections
    });

    // Import existing Cognito User Pool (enkai-dev)
    const userPool = props.cognitoUserPoolId
      ? cognito.UserPool.fromUserPoolId(this, 'EnkaiUserPool', props.cognitoUserPoolId)
      : undefined;

    const userPoolClient = userPool && props.cognitoClientId
      ? cognito.UserPoolClient.fromUserPoolClientId(this, 'EnkaiClient', props.cognitoClientId)
      : undefined;

    // HTTPS Listener
    const httpsListener = alb.addListener('HttpsListener', {
      port: 443,
      certificates: [certificate],
      protocol: elbv2.ApplicationProtocol.HTTPS,
      defaultAction: elbv2.ListenerAction.fixedResponse(403, {
        contentType: 'text/plain',
        messageBody: 'Forbidden',
      }),
    });

    // HTTP redirect to HTTPS
    alb.addListener('HttpListener', {
      port: 80,
      defaultAction: elbv2.ListenerAction.redirect({
        protocol: 'HTTPS',
        port: '443',
        permanent: true,
      }),
    });

    // Security group for ECS tasks
    const serviceSg = new ec2.SecurityGroup(this, 'FrankServiceSg', {
      vpc,
      description: 'Security group for Frank ECS service',
      allowAllOutbound: true,
    });

    // Allow ALB to connect to service (web port)
    serviceSg.addIngressRule(
      ec2.Peer.securityGroupId(alb.connections.securityGroups[0].securityGroupId),
      ec2.Port.tcpRange(7680, 7683),
      'Allow ALB to reach Frank ports'
    );

    // Allow ALB to reach health check port (explicit egress rule)
    alb.connections.securityGroups[0].addEgressRule(
      serviceSg,
      ec2.Port.tcp(7683),
      'Allow ALB to reach health check port'
    );

    // Fargate Service
    const service = new ecs.FargateService(this, 'FrankService', {
      cluster,
      taskDefinition,
      desiredCount: 1,
      assignPublicIp: false,
      securityGroups: [serviceSg],
      vpcSubnets: { subnetType: ec2.SubnetType.PRIVATE_WITH_EGRESS },
      enableExecuteCommand: true,
      serviceName: 'frank',
    });

    // Target group for web port
    const webTargetGroup = new elbv2.ApplicationTargetGroup(this, 'FrankWebTarget', {
      vpc,
      port: 7680,
      protocol: elbv2.ApplicationProtocol.HTTP,
      targetType: elbv2.TargetType.IP,
      healthCheck: {
        path: '/health',
        port: '7683',
        protocol: elbv2.Protocol.HTTP,
        healthyHttpCodes: '200',
        interval: cdk.Duration.seconds(30),
        timeout: cdk.Duration.seconds(10),
        healthyThresholdCount: 2,
        unhealthyThresholdCount: 5,
      },
      deregistrationDelay: cdk.Duration.seconds(30),
    });

    // Target group for status endpoint (context panel)
    const statusTargetGroup = new elbv2.ApplicationTargetGroup(this, 'FrankStatusTarget', {
      vpc,
      port: 7683,
      protocol: elbv2.ApplicationProtocol.HTTP,
      targetType: elbv2.TargetType.IP,
      healthCheck: {
        path: '/health',
        port: '7683',
        protocol: elbv2.Protocol.HTTP,
        healthyHttpCodes: '200',
        interval: cdk.Duration.seconds(30),
        timeout: cdk.Duration.seconds(10),
        healthyThresholdCount: 2,
        unhealthyThresholdCount: 5,
      },
      deregistrationDelay: cdk.Duration.seconds(30),
    });

    // Target group for Claude ttyd terminal
    const claudeTargetGroup = new elbv2.ApplicationTargetGroup(this, 'FrankClaudeTarget', {
      vpc,
      port: 7681,
      protocol: elbv2.ApplicationProtocol.HTTP,
      targetType: elbv2.TargetType.IP,
      healthCheck: {
        path: '/health',
        port: '7683',
        protocol: elbv2.Protocol.HTTP,
        healthyHttpCodes: '200',
        interval: cdk.Duration.seconds(30),
        timeout: cdk.Duration.seconds(10),
        healthyThresholdCount: 2,
        unhealthyThresholdCount: 5,
      },
      deregistrationDelay: cdk.Duration.seconds(30),
      stickinessCookieDuration: cdk.Duration.hours(1),
    });

    // Target group for Bash ttyd terminal
    const bashTargetGroup = new elbv2.ApplicationTargetGroup(this, 'FrankBashTarget', {
      vpc,
      port: 7682,
      protocol: elbv2.ApplicationProtocol.HTTP,
      targetType: elbv2.TargetType.IP,
      healthCheck: {
        path: '/health',
        port: '7683',
        protocol: elbv2.Protocol.HTTP,
        healthyHttpCodes: '200',
        interval: cdk.Duration.seconds(30),
        timeout: cdk.Duration.seconds(10),
        healthyThresholdCount: 2,
        unhealthyThresholdCount: 5,
      },
      deregistrationDelay: cdk.Duration.seconds(30),
      stickinessCookieDuration: cdk.Duration.hours(1),
    });

    // Attach service to target groups with explicit container port mapping
    webTargetGroup.addTarget(service.loadBalancerTarget({
      containerName: 'frank',
      containerPort: 7680,
    }));
    statusTargetGroup.addTarget(service.loadBalancerTarget({
      containerName: 'frank',
      containerPort: 7683,
    }));
    claudeTargetGroup.addTarget(service.loadBalancerTarget({
      containerName: 'frank',
      containerPort: 7681,
    }));
    bashTargetGroup.addTarget(service.loadBalancerTarget({
      containerName: 'frank',
      containerPort: 7682,
    }));

    // Cognito authentication action (if configured)
    const cognitoDomain = props.cognitoDomain || 'enkai-dev';
    const authAction = userPool && userPoolClient
      ? new elbv2Actions.AuthenticateCognitoAction({
          userPool,
          userPoolClient,
          userPoolDomain: cognito.UserPoolDomain.fromDomainName(this, 'EnkaiDomain', `${cognitoDomain}.auth.${this.region}.amazoncognito.com`),
          next: elbv2.ListenerAction.forward([webTargetGroup]),
        })
      : elbv2.ListenerAction.forward([webTargetGroup]);

    // Cognito auth actions for terminal routes
    const claudeAuthAction = userPool && userPoolClient
      ? new elbv2Actions.AuthenticateCognitoAction({
          userPool,
          userPoolClient,
          userPoolDomain: cognito.UserPoolDomain.fromDomainName(this, 'EnkaiDomainClaude', `${cognitoDomain}.auth.${this.region}.amazoncognito.com`),
          next: elbv2.ListenerAction.forward([claudeTargetGroup]),
        })
      : elbv2.ListenerAction.forward([claudeTargetGroup]);

    const bashAuthAction = userPool && userPoolClient
      ? new elbv2Actions.AuthenticateCognitoAction({
          userPool,
          userPoolClient,
          userPoolDomain: cognito.UserPoolDomain.fromDomainName(this, 'EnkaiDomainBash', `${cognitoDomain}.auth.${this.region}.amazoncognito.com`),
          next: elbv2.ListenerAction.forward([bashTargetGroup]),
        })
      : elbv2.ListenerAction.forward([bashTargetGroup]);

    // Add listener rules
    // Health endpoint - no auth, routes to dedicated health server (port 7683)
    httpsListener.addAction('HealthRule', {
      priority: 9,
      conditions: [elbv2.ListenerCondition.pathPatterns(['/health'])],
      action: elbv2.ListenerAction.forward([statusTargetGroup]),
    });

    // Status endpoint - no auth required (for context panel API calls)
    // Routes to the web server (port 7680) which serves /status and /status/detailed
    httpsListener.addAction('StatusRule', {
      priority: 10,
      conditions: [elbv2.ListenerCondition.pathPatterns(['/status', '/status/*'])],
      action: elbv2.ListenerAction.forward([webTargetGroup]),
    });

    // Claude terminal - with Cognito auth
    httpsListener.addAction('ClaudeRule', {
      priority: 15,
      conditions: [elbv2.ListenerCondition.pathPatterns(['/claude', '/claude/*'])],
      action: claudeAuthAction,
    });

    // Bash terminal - with Cognito auth
    httpsListener.addAction('BashRule', {
      priority: 16,
      conditions: [elbv2.ListenerCondition.pathPatterns(['/bash', '/bash/*'])],
      action: bashAuthAction,
    });

    // Main app catch-all - with Cognito auth if configured
    // Priority 49000 so profile rules (100-999) are evaluated first
    httpsListener.addAction('MainRule', {
      priority: 49000,
      conditions: [elbv2.ListenerCondition.pathPatterns(['/*'])],
      action: authAction,
    });

    // Route 53 Records
    const hostedZone = route53.HostedZone.fromHostedZoneAttributes(this, 'Zone', {
      hostedZoneId: props.hostedZoneId,
      zoneName: 'digitaldevops.io',
    });

    // Main domain: frank.digitaldevops.io
    // (No wildcard needed - using path-based routing instead of subdomains)
    new route53.ARecord(this, 'FrankDns', {
      zone: hostedZone,
      recordName: 'frank',
      target: route53.RecordTarget.fromAlias(new route53Targets.LoadBalancerTarget(alb)),
    });

    // =========================================================================
    // Launch Page API (Lambda + ALB integration)
    // =========================================================================

    // SSM Parameter to store profiles configuration
    const profilesParam = new ssm.StringParameter(this, 'ProfilesParam', {
      parameterName: '/frank/profiles',
      stringValue: '[]', // Empty array initially
      description: 'Frank profile configurations (JSON array)',
      tier: ssm.ParameterTier.STANDARD,
    });

    // Lambda function for the API
    const apiFunction = new lambdaNodejs.NodejsFunction(this, 'FrankApiFunction', {
      entry: path.join(__dirname, '../lambda/api/index.ts'),
      handler: 'handler',
      runtime: lambda.Runtime.NODEJS_18_X,
      timeout: cdk.Duration.seconds(90), // Needs time to wait for task IP registration
      memorySize: 256,
      environment: {
        ECS_CLUSTER: 'frank',
        ECS_SERVICE: 'frank',
        DOMAIN: props.domainName,
        PROFILES_PARAM: profilesParam.parameterName,
        ALB_NAME: 'frank-alb',
        // Cognito config for profile route authentication
        COGNITO_USER_POOL_ARN: userPool ? `arn:aws:cognito-idp:${this.region}:${this.account}:userpool/${props.cognitoUserPoolId}` : '',
        COGNITO_CLIENT_ID: props.cognitoClientId || '',
        COGNITO_DOMAIN: `${cognitoDomain}.auth.${this.region}.amazoncognito.com`,
      },
      bundling: {
        externalModules: ['@aws-sdk/*'], // Use Lambda runtime SDK
        minify: true,
        sourceMap: true,
      },
    });

    // Grant Lambda permissions
    profilesParam.grantRead(apiFunction);
    profilesParam.grantWrite(apiFunction);

    // ECS permissions
    apiFunction.addToRolePolicy(new iam.PolicyStatement({
      actions: [
        'ecs:ListTasks',
        'ecs:DescribeTasks',
        'ecs:RunTask',
        'ecs:StopTask',
        'ecs:DescribeServices',
        'ecs:TagResource',
      ],
      resources: ['*'],
    }));

    // Pass role for ECS task
    apiFunction.addToRolePolicy(new iam.PolicyStatement({
      actions: ['iam:PassRole'],
      resources: [
        taskDefinition.taskRole.roleArn,
        taskDefinition.executionRole!.roleArn,
      ],
    }));

    // ALB permissions for dynamic target groups
    apiFunction.addToRolePolicy(new iam.PolicyStatement({
      actions: [
        'elasticloadbalancing:DescribeLoadBalancers',
        'elasticloadbalancing:DescribeListeners',
        'elasticloadbalancing:DescribeTargetGroups',
        'elasticloadbalancing:DescribeRules',
        'elasticloadbalancing:CreateTargetGroup',
        'elasticloadbalancing:CreateRule',
        'elasticloadbalancing:DeleteTargetGroup',
        'elasticloadbalancing:DeleteRule',
        'elasticloadbalancing:RegisterTargets',
        'elasticloadbalancing:DeregisterTargets',
        'elasticloadbalancing:AddTags',
      ],
      resources: ['*'],
    }));

    // Cognito permissions for creating auth rules
    apiFunction.addToRolePolicy(new iam.PolicyStatement({
      actions: [
        'cognito-idp:DescribeUserPoolClient',
      ],
      resources: ['*'],
    }));

    // Target group for Lambda
    const apiTargetGroup = new elbv2.ApplicationTargetGroup(this, 'FrankApiTarget', {
      targetType: elbv2.TargetType.LAMBDA,
      targets: [new elbv2Targets.LambdaTarget(apiFunction)],
    });

    // Add API routes to the listener (before the main rule)
    // API endpoint - with Cognito auth
    const apiAuthAction = userPool && userPoolClient
      ? new elbv2Actions.AuthenticateCognitoAction({
          userPool,
          userPoolClient,
          userPoolDomain: cognito.UserPoolDomain.fromDomainName(this, 'EnkaiDomainApi', `${cognitoDomain}.auth.${this.region}.amazoncognito.com`),
          next: elbv2.ListenerAction.forward([apiTargetGroup]),
        })
      : elbv2.ListenerAction.forward([apiTargetGroup]);

    httpsListener.addAction('ApiRule', {
      priority: 5,
      conditions: [elbv2.ListenerCondition.pathPatterns(['/api', '/api/*'])],
      action: apiAuthAction,
    });

    // Launch page at root (only for frank.digitaldevops.io, not subdomains)
    const launchAuthAction = userPool && userPoolClient
      ? new elbv2Actions.AuthenticateCognitoAction({
          userPool,
          userPoolClient,
          userPoolDomain: cognito.UserPoolDomain.fromDomainName(this, 'EnkaiDomainLaunch', `${cognitoDomain}.auth.${this.region}.amazoncognito.com`),
          next: elbv2.ListenerAction.forward([apiTargetGroup]),
        })
      : elbv2.ListenerAction.forward([apiTargetGroup]);

    httpsListener.addAction('LaunchRule', {
      priority: 6,
      conditions: [
        elbv2.ListenerCondition.hostHeaders([props.domainName]),
        elbv2.ListenerCondition.pathPatterns(['/', '/launch']),
      ],
      action: launchAuthAction,
    });

    // =========================================================================
    // Analytics Dashboard Lambda
    // =========================================================================
    const dashboardFunction = new lambdaNodejs.NodejsFunction(this, 'DashboardFunction', {
      entry: path.join(__dirname, '../lambda/dashboard/index.ts'),
      handler: 'handler',
      runtime: lambda.Runtime.NODEJS_18_X,
      timeout: cdk.Duration.seconds(30),
      memorySize: 512,
      environment: {
        ANALYTICS_BUCKET: analyticsBucket.bucketName,
        PROFILES_PARAM: profilesParam.parameterName,
      },
      bundling: {
        externalModules: ['@aws-sdk/*'],
        minify: true,
        sourceMap: true,
      },
    });

    // Grant dashboard read access to analytics bucket and profiles
    analyticsBucket.grantRead(dashboardFunction);
    profilesParam.grantRead(dashboardFunction);

    // Dashboard target group
    const dashboardTargetGroup = new elbv2.ApplicationTargetGroup(this, 'DashboardTarget', {
      targetType: elbv2.TargetType.LAMBDA,
      targets: [new elbv2Targets.LambdaTarget(dashboardFunction)],
    });

    // Dashboard route - with Cognito auth
    const dashboardAuthAction = userPool && userPoolClient
      ? new elbv2Actions.AuthenticateCognitoAction({
          userPool,
          userPoolClient,
          userPoolDomain: cognito.UserPoolDomain.fromDomainName(this, 'EnkaiDomainDashboard', `${cognitoDomain}.auth.${this.region}.amazoncognito.com`),
          next: elbv2.ListenerAction.forward([dashboardTargetGroup]),
        })
      : elbv2.ListenerAction.forward([dashboardTargetGroup]);

    httpsListener.addAction('DashboardRule', {
      priority: 7,
      conditions: [elbv2.ListenerCondition.pathPatterns(['/dashboard', '/dashboard/*'])],
      action: dashboardAuthAction,
    });

    // =========================================================================
    // Analytics Pipeline Lambda (Daily Aggregation)
    // =========================================================================
    const analyticsFunction = new lambdaNodejs.NodejsFunction(this, 'AnalyticsFunction', {
      entry: path.join(__dirname, '../lambda/analytics/index.ts'),
      handler: 'handler',
      runtime: lambda.Runtime.NODEJS_18_X,
      timeout: cdk.Duration.minutes(5),
      memorySize: 1024,
      environment: {
        ANALYTICS_BUCKET: analyticsBucket.bucketName,
      },
      bundling: {
        externalModules: ['@aws-sdk/*'],
        minify: true,
        sourceMap: true,
      },
    });

    // Grant analytics function read/write access to bucket
    analyticsBucket.grantReadWrite(analyticsFunction);

    // Schedule analytics to run daily at 2 AM UTC
    new events.Rule(this, 'AnalyticsSchedule', {
      schedule: events.Schedule.cron({ hour: '2', minute: '0' }),
      targets: [new eventsTargets.LambdaFunction(analyticsFunction)],
      description: 'Daily analytics aggregation for Frank prompts',
    });

    // =========================================================================
    // Codex Worker Task Definition (headless, no ALB/web terminal)
    // =========================================================================
    const codexTaskDefinition = new ecs.FargateTaskDefinition(this, 'FrankCodexWorker', {
      memoryLimitMiB: 2048,
      cpu: 1024,
      runtimePlatform: {
        cpuArchitecture: ecs.CpuArchitecture.X86_64,
        operatingSystemFamily: ecs.OperatingSystemFamily.LINUX,
      },
    });

    // Codex worker needs the same base permissions as the main task role
    // for EFS access, CloudWatch logging, and git operations
    analyticsBucket.grantWrite(codexTaskDefinition.taskRole);

    codexTaskDefinition.taskRole.addToPrincipalPolicy(new iam.PolicyStatement({
      actions: [
        'elasticfilesystem:ClientMount',
        'elasticfilesystem:ClientWrite',
        'elasticfilesystem:ClientRootAccess',
      ],
      resources: ['*'],
    }));

    // Codex worker container
    const codexContainer = codexTaskDefinition.addContainer('codex-worker', {
      image: ecs.ContainerImage.fromAsset('..', {
        file: 'build/Dockerfile.codex',
        exclude: ['cdk', 'cdk.out', 'node_modules', '.git'],
      }),
      logging: ecs.LogDrivers.awsLogs({
        logGroup,
        streamPrefix: 'codex-worker',
      }),
      environment: {
        CONTAINER_NAME: 'codex-worker',
        GIT_REPO: '',
        GIT_BRANCH: 'main',
        TASK_PROMPT: '',
        CODEX_MODEL: 'codex-mini',
      },
      secrets: {
        OPENAI_API_KEY: ecs.Secret.fromSecretsManager(openaiApiKeySecret),
        GITHUB_TOKEN: ecs.Secret.fromSecretsManager(githubTokenSecret),
      },
      // No port mappings - headless worker, no web UI
    });

    // Grant Lambda permission to pass Codex task roles for RunTask
    apiFunction.addToRolePolicy(new iam.PolicyStatement({
      actions: ['iam:PassRole'],
      resources: [
        codexTaskDefinition.taskRole.roleArn,
        codexTaskDefinition.executionRole!.roleArn,
      ],
    }));

    // Outputs
    new cdk.CfnOutput(this, 'ServiceUrl', {
      value: `https://${props.domainName}`,
      description: 'Frank service URL',
    });

    new cdk.CfnOutput(this, 'AlbDnsName', {
      value: alb.loadBalancerDnsName,
      description: 'ALB DNS name',
    });

    new cdk.CfnOutput(this, 'GitHubTokenSecretArn', {
      value: githubTokenSecret.secretArn,
      description: 'GitHub token secret ARN - update with: aws secretsmanager put-secret-value --secret-id /frank/github-token --secret-string "$(gh auth token)"',
    });

    new cdk.CfnOutput(this, 'ClaudeCredentialsSecretArn', {
      value: claudeCredentialsSecret.secretArn,
      description: 'Claude credentials secret ARN - update with: aws secretsmanager put-secret-value --secret-id /frank/claude-credentials --secret-string "$(cat ~/.claude/.credentials.json)"',
    });

    new cdk.CfnOutput(this, 'PnyxApiKeySecretArn', {
      value: pnyxApiKeySecret.secretArn,
      description: 'Pnyx API key secret ARN - update with: aws secretsmanager put-secret-value --secret-id /frank/pnyx-api-key --secret-string "pnyx_..."',
    });

    new cdk.CfnOutput(this, 'AnalyticsBucketName', {
      value: analyticsBucket.bucketName,
      description: 'S3 bucket for prompt analytics',
    });

    new cdk.CfnOutput(this, 'DashboardUrl', {
      value: `https://${props.domainName}/dashboard`,
      description: 'Analytics dashboard URL',
    });

    new cdk.CfnOutput(this, 'OpenAiApiKeySecretArn', {
      value: openaiApiKeySecret.secretArn,
      description: 'OpenAI API key secret ARN - update with: aws secretsmanager put-secret-value --secret-id /frank/openai-api-key --secret-string "sk-..."',
    });

    new cdk.CfnOutput(this, 'CodexTaskDefinitionArn', {
      value: codexTaskDefinition.taskDefinitionArn,
      description: 'Codex worker task definition ARN for CLI usage',
    });
  }
}
