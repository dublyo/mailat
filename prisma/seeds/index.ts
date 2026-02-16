/**
 * Prisma seed file
 * Run with: pnpm db:seed
 */

import { PrismaClient } from '@prisma/client';
import { createHash, randomBytes } from 'crypto';

const prisma = new PrismaClient();

// Hash password using SHA256 (for demo - use bcrypt in production)
function hashPassword(password: string): string {
  return createHash('sha256').update(password).digest('hex');
}

// Generate API key
function generateApiKey(): { key: string; prefix: string; hash: string } {
  const key = `ue_${randomBytes(24).toString('hex')}`;
  const prefix = key.substring(0, 10);
  const hash = createHash('sha256').update(key).digest('hex');
  return { key, prefix, hash };
}

async function main() {
  console.log('🌱 Seeding database...\n');

  // Clean existing data (in order of dependencies)
  console.log('Cleaning existing data...');
  await prisma.webhookCall.deleteMany();
  await prisma.webhook.deleteMany();
  await prisma.deliveryEvent.deleteMany();
  await prisma.email.deleteMany();
  await prisma.suppression.deleteMany();
  await prisma.warmupProgress.deleteMany();
  await prisma.messageMetadata.deleteMany();
  await prisma.threadGroup.deleteMany();
  await prisma.listContact.deleteMany();
  await prisma.contact.deleteMany();
  await prisma.campaign.deleteMany();
  await prisma.list.deleteMany();
  await prisma.template.deleteMany();
  await prisma.identity.deleteMany();
  await prisma.domainDnsRecord.deleteMany();
  await prisma.domain.deleteMany();
  await prisma.apiKey.deleteMany();
  await prisma.user.deleteMany();
  await prisma.organization.deleteMany();

  // Create organization
  console.log('Creating organization...');
  const org = await prisma.organization.create({
    data: {
      name: 'Mailat',
      slug: 'mailat',
      plan: 'pro',
      maxDomains: 10,
      maxUsers: 50,
      maxContacts: 100000,
      monthlyEmailLimit: 1000000,
      settings: {
        timezone: 'UTC',
        dateFormat: 'YYYY-MM-DD',
      },
    },
  });
  console.log(`  ✅ Organization: ${org.name} (${org.slug})`);

  // Create admin user
  console.log('Creating users...');
  const adminUser = await prisma.user.create({
    data: {
      orgId: org.id,
      email: 'admin@example.com',
      passwordHash: hashPassword('admin123'),
      name: 'Admin User',
      role: 'owner',
      emailVerified: true,
      emailVerifiedAt: new Date(),
    },
  });
  console.log(`  ✅ Admin user: ${adminUser.email}`);

  // Create test user
  const testUser = await prisma.user.create({
    data: {
      orgId: org.id,
      email: 'test@example.com',
      passwordHash: hashPassword('test123'),
      name: 'Test User',
      role: 'member',
      emailVerified: true,
      emailVerifiedAt: new Date(),
    },
  });
  console.log(`  ✅ Test user: ${testUser.email}`);

  // Create API key
  console.log('Creating API key...');
  const apiKeyData = generateApiKey();
  const apiKey = await prisma.apiKey.create({
    data: {
      orgId: org.id,
      userId: adminUser.id,
      name: 'Default API Key',
      keyPrefix: apiKeyData.prefix,
      keyHash: apiKeyData.hash,
      permissions: ['emails:send', 'emails:read', 'contacts:read', 'contacts:write'],
      rateLimit: 100,
    },
  });
  console.log(`  ✅ API Key created`);
  console.log(`  📋 Full API Key (save this): ${apiKeyData.key}`);

  // Create domain
  console.log('Creating domain...');
  const domain = await prisma.domain.create({
    data: {
      orgId: org.id,
      name: 'example.com',
      verificationToken: randomBytes(16).toString('hex'),
      status: 'active',
      mxVerified: true,
      spfVerified: true,
      dkimVerified: true,
      dmarcVerified: true,
      dkimSelector: 'mail',
      openTracking: true,
      clickTracking: true,
    },
  });
  console.log(`  ✅ Domain: ${domain.name}`);

  // Create DNS records
  console.log('Creating DNS records...');
  await prisma.domainDnsRecord.createMany({
    data: [
      {
        domainId: domain.id,
        recordType: 'MX',
        hostname: 'example.com',
        expectedValue: '10 mail.example.com',
        verified: true,
      },
      {
        domainId: domain.id,
        recordType: 'TXT',
        hostname: 'example.com',
        expectedValue: 'v=spf1 mx a ~all',
        verified: true,
      },
      {
        domainId: domain.id,
        recordType: 'TXT',
        hostname: 'mail._domainkey.example.com',
        expectedValue: 'v=DKIM1; k=rsa; p=...',
        verified: true,
      },
      {
        domainId: domain.id,
        recordType: 'TXT',
        hostname: '_dmarc.example.com',
        expectedValue: 'v=DMARC1; p=quarantine; rua=mailto:dmarc@example.com',
        verified: true,
      },
    ],
  });
  console.log(`  ✅ DNS records created`);

  // Create identities
  console.log('Creating identities...');
  const identity1 = await prisma.identity.create({
    data: {
      userId: adminUser.id,
      domainId: domain.id,
      email: 'hello@example.com',
      displayName: 'Mailat',
      isDefault: true,
      canSend: true,
      canReceive: true,
      passwordHash: hashPassword('mailbox123'),
      signatureHtml: '<p>Best regards,<br>Mailat Team</p>',
      signatureText: 'Best regards,\nMailat Team',
    },
  });
  console.log(`  ✅ Identity: ${identity1.email}`);

  const identity2 = await prisma.identity.create({
    data: {
      userId: adminUser.id,
      domainId: domain.id,
      email: 'support@example.com',
      displayName: 'Support',
      canSend: true,
      canReceive: true,
      passwordHash: hashPassword('mailbox123'),
    },
  });
  console.log(`  ✅ Identity: ${identity2.email}`);

  // Create contact list
  console.log('Creating contact list...');
  const list = await prisma.list.create({
    data: {
      orgId: org.id,
      name: 'Newsletter Subscribers',
      description: 'Main newsletter subscription list',
      type: 'static',
    },
  });
  console.log(`  ✅ List: ${list.name}`);

  // Create contacts
  console.log('Creating contacts...');
  const contacts = await Promise.all([
    prisma.contact.create({
      data: {
        orgId: org.id,
        email: 'john@example.com',
        firstName: 'John',
        lastName: 'Doe',
        status: 'active',
        consentSource: 'signup_form',
        consentTimestamp: new Date(),
        attributes: { company: 'Acme Inc', role: 'Developer' },
      },
    }),
    prisma.contact.create({
      data: {
        orgId: org.id,
        email: 'jane@example.com',
        firstName: 'Jane',
        lastName: 'Smith',
        status: 'active',
        consentSource: 'signup_form',
        consentTimestamp: new Date(),
        attributes: { company: 'Tech Corp', role: 'Designer' },
      },
    }),
    prisma.contact.create({
      data: {
        orgId: org.id,
        email: 'bob@example.com',
        firstName: 'Bob',
        lastName: 'Wilson',
        status: 'active',
        consentSource: 'api',
        consentTimestamp: new Date(),
        attributes: { company: 'StartupXYZ', role: 'CEO' },
      },
    }),
  ]);
  console.log(`  ✅ Created ${contacts.length} contacts`);

  // Add contacts to list
  await prisma.listContact.createMany({
    data: contacts.map((contact) => ({
      listId: list.id,
      contactId: contact.id,
    })),
  });
  await prisma.list.update({
    where: { id: list.id },
    data: { contactCount: contacts.length },
  });

  // Create email template
  console.log('Creating template...');
  const template = await prisma.template.create({
    data: {
      orgId: org.id,
      name: 'Welcome Email',
      description: 'Welcome email for new subscribers',
      category: 'transactional',
      htmlContent: `
<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <title>Welcome to {{companyName}}</title>
</head>
<body style="font-family: Arial, sans-serif; max-width: 600px; margin: 0 auto;">
  <h1>Welcome, {{firstName}}!</h1>
  <p>Thank you for joining {{companyName}}. We're excited to have you on board.</p>
  <p>Here's what you can do next:</p>
  <ul>
    <li>Explore our features</li>
    <li>Set up your profile</li>
    <li>Connect with our community</li>
  </ul>
  <p>If you have any questions, feel free to reply to this email.</p>
  <p>Best regards,<br>The {{companyName}} Team</p>
</body>
</html>
      `.trim(),
      textContent: `
Welcome, {{firstName}}!

Thank you for joining {{companyName}}. We're excited to have you on board.

Here's what you can do next:
- Explore our features
- Set up your profile
- Connect with our community

If you have any questions, feel free to reply to this email.

Best regards,
The {{companyName}} Team
      `.trim(),
      variablesSchema: {
        type: 'object',
        properties: {
          firstName: { type: 'string' },
          companyName: { type: 'string' },
        },
        required: ['firstName', 'companyName'],
      },
    },
  });
  console.log(`  ✅ Template: ${template.name}`);

  // Summary
  console.log('\n' + '='.repeat(50));
  console.log('🎉 Seed completed successfully!\n');
  console.log('Summary:');
  console.log(`  • Organization: ${org.name}`);
  console.log(`  • Users: 2`);
  console.log(`  • Domain: ${domain.name}`);
  console.log(`  • Identities: 2`);
  console.log(`  • Contacts: ${contacts.length}`);
  console.log(`  • Lists: 1`);
  console.log(`  • Templates: 1`);
  console.log('\nCredentials:');
  console.log(`  • Admin: admin@example.com / admin123`);
  console.log(`  • Test: test@example.com / test123`);
  console.log(`  • API Key: ${apiKeyData.key}`);
  console.log('='.repeat(50));
}

main()
  .catch((e) => {
    console.error('❌ Seed failed:', e);
    process.exit(1);
  })
  .finally(async () => {
    await prisma.$disconnect();
  });
