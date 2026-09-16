# Grants the attached service read/write on this database. Shipped by the
# postgres kind and copied into each dependent service's bundle, so the
# policy lives with the kind that knows what access it needs.
resource "aws_iam_policy" "DB_ACCESS" {
  name = "DB_NAME-access"

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["rds-db:connect"]
      Resource = "arn:aws:rds-db:REGION:acme:dbuser:DB_NAME/*"
    }]
  })
}
