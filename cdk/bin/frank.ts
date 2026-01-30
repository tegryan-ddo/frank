#!/usr/bin/env node
import 'source-map-support/register';
import * as cdk from 'aws-cdk-lib';
import { FrankStack } from '../lib/frank-stack';
import { FrankPipelineStack } from '../lib/frank-pipeline-stack';

const app = new cdk.App();

new FrankStack(app, 'FrankStack', {
  env: {
    account: process.env.CDK_DEFAULT_ACCOUNT,
    region: process.env.CDK_DEFAULT_REGION || 'us-east-1',
  },

  // Configuration
  domainName: 'frank.digitaldevops.io',
  hostedZoneId: 'Z3OKT7D3Q3TASV',
  // Certificate covers both frank.digitaldevops.io and *.frank.digitaldevops.io
  certificateArn: 'arn:aws:acm:us-east-1:882384879235:certificate/9d7e2d1a-96a2-44ab-8401-0c3ad606725b',

  // Cognito authentication (enkai-dev user pool)
  cognitoUserPoolId: 'us-east-1_zlw7qsJMJ',
  cognitoClientId: '66gneinplh8gnp8fjj5rrpsrsk',
  cognitoDomain: 'enkai-dev',
});

new FrankPipelineStack(app, 'FrankPipelineStack', {
  env: {
    account: process.env.CDK_DEFAULT_ACCOUNT,
    region: process.env.CDK_DEFAULT_REGION || 'us-east-1',
  },
});
