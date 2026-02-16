/**
 * Test connectivity to all external services
 * Run with: pnpm test:db
 */

import { config } from 'dotenv';
import { resolve } from 'path';

// Load environment variables
config({ path: resolve(__dirname, '../.env') });

interface TestResult {
  service: string;
  status: 'success' | 'failed';
  message: string;
  latency?: number;
}

async function testPostgreSQL(): Promise<TestResult> {
  const start = Date.now();
  try {
    const { PrismaClient } = await import('@prisma/client');
    const prisma = new PrismaClient();

    // Test connection with a simple query
    await prisma.$queryRaw`SELECT 1 as test`;
    await prisma.$disconnect();

    return {
      service: 'PostgreSQL',
      status: 'success',
      message: `Connected to ${process.env.DATABASE_URL?.split('@')[1]?.split('/')[0] || 'database'}`,
      latency: Date.now() - start
    };
  } catch (error) {
    return {
      service: 'PostgreSQL',
      status: 'failed',
      message: error instanceof Error ? error.message : 'Unknown error'
    };
  }
}

async function testRedis(): Promise<TestResult> {
  const start = Date.now();
  try {
    const Redis = (await import('ioredis')).default;
    const redis = new Redis(process.env.REDIS_URL || '');

    // Test connection with PING
    const pong = await redis.ping();
    await redis.quit();

    if (pong !== 'PONG') {
      throw new Error(`Expected PONG, got ${pong}`);
    }

    return {
      service: 'Redis',
      status: 'success',
      message: 'Connected and PING successful',
      latency: Date.now() - start
    };
  } catch (error) {
    return {
      service: 'Redis',
      status: 'failed',
      message: error instanceof Error ? error.message : 'Unknown error'
    };
  }
}

async function testTypesense(): Promise<TestResult> {
  const start = Date.now();
  try {
    const url = process.env.TYPESENSE_URL || 'http://localhost:8108';
    const apiKey = process.env.TYPESENSE_API_KEY || '';

    const response = await fetch(`${url}/health`, {
      headers: {
        'X-TYPESENSE-API-KEY': apiKey
      }
    });

    if (!response.ok) {
      throw new Error(`HTTP ${response.status}: ${response.statusText}`);
    }

    const data = await response.json();

    return {
      service: 'Typesense',
      status: 'success',
      message: `Health check: ${JSON.stringify(data)}`,
      latency: Date.now() - start
    };
  } catch (error) {
    return {
      service: 'Typesense',
      status: 'failed',
      message: error instanceof Error ? error.message : 'Unknown error'
    };
  }
}

async function main() {
  console.log('\n🔍 Testing connections to external services...\n');
  console.log('=' .repeat(60));

  const tests = [
    testPostgreSQL(),
    testRedis(),
    testTypesense()
  ];

  const results = await Promise.all(tests);

  let allPassed = true;

  for (const result of results) {
    const icon = result.status === 'success' ? '✅' : '❌';
    const latency = result.latency ? ` (${result.latency}ms)` : '';

    console.log(`\n${icon} ${result.service}${latency}`);
    console.log(`   ${result.message}`);

    if (result.status === 'failed') {
      allPassed = false;
    }
  }

  console.log('\n' + '='.repeat(60));

  if (allPassed) {
    console.log('\n🎉 All services connected successfully!\n');
    process.exit(0);
  } else {
    console.log('\n⚠️  Some services failed to connect. Check the errors above.\n');
    process.exit(1);
  }
}

main().catch(console.error);
