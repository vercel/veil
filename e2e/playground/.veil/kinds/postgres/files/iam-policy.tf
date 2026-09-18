# Grants the attached service read/write on this database. Shipped by the
# postgres kind and rewritten per consumer by attach-iam-policy, which
# edits it as data rather than as text.
resource "aws_iam_policy" "db_access" {
  name = "PLACEHOLDER-access"

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["rds-db:connect"]
      Resource = "arn:aws:rds-db:PLACEHOLDER-region:acme:dbuser:PLACEHOLDER/*"
    }]
  })
}


