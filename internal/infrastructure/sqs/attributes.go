package sqs

import (
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

func StringAttribute(value string) types.MessageAttributeValue {
	return types.MessageAttributeValue{DataType: aws.String("String"), StringValue: aws.String(value)}
}

func NumberAttribute(value int) types.MessageAttributeValue {
	return types.MessageAttributeValue{DataType: aws.String("Number"), StringValue: aws.String(strconv.Itoa(value))}
}
