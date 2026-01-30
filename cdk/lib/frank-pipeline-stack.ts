import * as cdk from 'aws-cdk-lib';
import { Construct } from 'constructs';
import { CodePipeline, CodePipelineSource, ShellStep } from 'aws-cdk-lib/pipelines';
import { FrankStack, FrankStackProps } from './frank-stack';

/**
 * Stage that wraps FrankStack for use in CDK Pipelines.
 */
class FrankStage extends cdk.Stage {
  constructor(scope: Construct, id: string, props: cdk.StageProps & { frankProps: Omit<FrankStackProps, keyof cdk.StackProps> }) {
    super(scope, id, props);

    new FrankStack(this, 'FrankStack', {
      stackName: 'FrankStack',
      ...props.frankProps,
    });
  }
}

export class FrankPipelineStack extends cdk.Stack {
  constructor(scope: Construct, id: string, props?: cdk.StackProps) {
    super(scope, id, props);

    const pipeline = new CodePipeline(this, 'Pipeline', {
      pipelineName: 'FrankPipeline',
      synth: new ShellStep('Synth', {
        input: CodePipelineSource.connection('tegryan-ddo/frank', 'main', {
          connectionArn: 'arn:aws:codeconnections:us-east-1:882384879235:connection/2b22aa6f-0fe4-47b8-b2a5-d79bf667c362',
        }),
        commands: [
          'cd cdk',
          'npm ci',
          'npx cdk synth',
        ],
        primaryOutputDirectory: 'cdk/cdk.out',
      }),
      dockerEnabledForSynth: true,
      dockerEnabledForSelfMutation: true,
    });

    pipeline.addStage(new FrankStage(this, 'Deploy', {
      env: {
        account: this.account,
        region: 'us-east-1',
      },
      frankProps: {
        domainName: 'frank.digitaldevops.io',
        hostedZoneId: 'Z3OKT7D3Q3TASV',
        certificateArn: 'arn:aws:acm:us-east-1:882384879235:certificate/9d7e2d1a-96a2-44ab-8401-0c3ad606725b',
        cognitoUserPoolId: 'us-east-1_zlw7qsJMJ',
        cognitoClientId: '66gneinplh8gnp8fjj5rrpsrsk',
        cognitoDomain: 'enkai-dev',
      },
    }));
  }
}
