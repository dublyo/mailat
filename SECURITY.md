# Security Policy

## Supported Versions

We release security updates for the following versions:

| Version | Supported          |
| ------- | ------------------ |
| 1.x     | :white_check_mark: |
| < 1.0   | :x:                |

## Reporting a Vulnerability

**DO NOT** create public GitHub issues for security vulnerabilities.

### How to Report

Email: **security@mailat.co**

Include in your report:

1. **Description** of the vulnerability
2. **Steps to reproduce** the issue
3. **Impact assessment** (who is affected, severity)
4. **Suggested fix** (if you have one)
5. **Your contact information**

### What to Expect

- **Initial Response**: Within 48 hours
- **Status Update**: Within 7 days
- **Fix Timeline**: Depends on severity
  - Critical: 1-7 days
  - High: 7-14 days
  - Medium: 14-30 days
  - Low: 30-90 days

### Our Commitment

- We will acknowledge your email within 48 hours
- We will provide regular updates on our progress
- We will credit you in the security advisory (unless you prefer to remain anonymous)
- We will notify you when the vulnerability is fixed

### Security Best Practices

When deploying Mailat, we recommend:

1. **Use HTTPS only** - Never expose the API over HTTP
2. **Rotate secrets regularly** - JWT secrets, API keys, database passwords
3. **Keep dependencies updated** - Run `pnpm update` and `go get -u` regularly
4. **Use strong passwords** - Especially for admin accounts
5. **Enable 2FA** - For all administrative users
6. **Monitor logs** - Watch for suspicious activity
7. **Limit API access** - Use API keys with minimal necessary permissions
8. **Backup regularly** - Maintain encrypted database backups
9. **Use environment variables** - Never commit secrets to git
10. **Review audit logs** - Check compliance and security logs regularly

### Known Security Considerations

- **AWS Credentials**: Store AWS credentials securely (use IAM roles when possible)
- **Database Access**: Use read-only credentials for read operations
- **Email Content**: Sanitize HTML content to prevent XSS
- **Rate Limiting**: Configure appropriate rate limits for your use case
- **DKIM Keys**: Protect private DKIM keys with appropriate file permissions

### Security Updates

We will publish security advisories at:
- https://github.com/dublyo/mailat/security/advisories

Subscribe to our repository to receive security notifications.

## Bug Bounty

We currently do not have a formal bug bounty program, but we greatly appreciate responsible disclosure and will credit researchers in our security advisories.

## Contact

For non-security issues, please use:
- GitHub Issues: https://github.com/dublyo/mailat/issues
- GitHub Discussions: https://github.com/dublyo/mailat/discussions

For security issues only: security@mailat.co

---

Thank you for helping keep Mailat secure!
