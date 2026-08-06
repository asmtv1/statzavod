#!/usr/bin/env node

import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import ts from 'typescript'

const projectRoot = process.cwd()
const sourceRoot = path.join(projectRoot, 'src')
const dictionaryFile = path.join(sourceRoot, 'shared/i18n/I18nProvider.tsx')
const cyrillic = /\p{Script=Cyrillic}/u

// These pages own explicit RU/EN content selected by their localized route.
// The authenticated application must use the shared t() function instead.
const bilingualFiles = new Set([
  'src/features/public/PublicSite.tsx',
  'src/features/public/RequestAccessPage.tsx',
  'src/features/legal/LegalPages.tsx',
])

function relative(file) {
  return path.relative(projectRoot, file).split(path.sep).join('/')
}

function walk(directory) {
  return fs.readdirSync(directory, { withFileTypes: true }).flatMap(entry => {
    const target = path.join(directory, entry.name)
    if (entry.isDirectory()) return walk(target)
    return /\.(?:ts|tsx)$/.test(entry.name) && !entry.name.endsWith('.d.ts') ? [target] : []
  })
}

function parse(file) {
  const source = fs.readFileSync(file, 'utf8')
  const kind = file.endsWith('.tsx') ? ts.ScriptKind.TSX : ts.ScriptKind.TS
  return ts.createSourceFile(file, source, ts.ScriptTarget.Latest, true, kind)
}

function staticText(node) {
  if (ts.isStringLiteralLike(node)) return node.text
  return null
}

function isTCall(node) {
  if (!ts.isCallExpression(node)) return false
  return ts.isIdentifier(node.expression)
    ? node.expression.text === 't'
    : ts.isPropertyAccessExpression(node.expression) && node.expression.name.text === 't'
}

function isStaticTKey(node) {
  let current = node
  while (ts.isParenthesizedExpression(current.parent)) current = current.parent
  return ts.isCallExpression(current.parent) && isTCall(current.parent) && current.parent.arguments[0] === current
}

function isInsideJsx(node) {
  for (let current = node.parent; current; current = current.parent) {
    if (ts.isJsxExpression(current) || ts.isJsxAttribute(current)) return true
    if (ts.isSourceFile(current)) return false
  }
  return false
}

function isInsideCallArgument(node) {
  let child = node
  for (let current = node.parent; current; child = current, current = current.parent) {
    if (ts.isFunctionLike(current)) return false
    if ((ts.isCallExpression(current) || ts.isNewExpression(current)) && current.arguments?.some(argument => argument === child)) {
      return !ts.isCallExpression(current) || !isTCall(current)
    }
    if (ts.isSourceFile(current)) return false
  }
  return false
}

function location(sourceFile, node) {
  const point = sourceFile.getLineAndCharacterOfPosition(node.getStart(sourceFile))
  return `${relative(sourceFile.fileName)}:${point.line + 1}:${point.character + 1}`
}

function readEnglishDictionary(sourceFile) {
  const translations = new Map()
  let dictionaryFound = false

  function visit(node) {
    if (ts.isVariableDeclaration(node) && ts.isIdentifier(node.name) && node.name.text === 'en' && node.initializer && ts.isObjectLiteralExpression(node.initializer)) {
      dictionaryFound = true
      for (const property of node.initializer.properties) {
        if (!ts.isPropertyAssignment(property)) continue
        const key = ts.isComputedPropertyName(property.name) ? null : staticText(property.name)
        const value = staticText(property.initializer)
        if (key !== null && value !== null) translations.set(key, value)
      }
      return
    }
    ts.forEachChild(node, visit)
  }

  visit(sourceFile)
  if (!dictionaryFound) throw new Error(`English dictionary "en" was not found in ${relative(sourceFile.fileName)}`)
  return translations
}

function shouldCheckVisibleStrings(file) {
  const name = relative(file)
  if (name === relative(dictionaryFile) || bilingualFiles.has(name)) return false
  return name.startsWith('src/app/') || name.startsWith('src/features/') || name.startsWith('src/shared/ui/')
}

const files = walk(sourceRoot)
const translations = readEnglishDictionary(parse(dictionaryFile))
const failures = []
const requiredEnglishKeys = new Map()

for (const file of files) {
  const sourceFile = parse(file)
  const checkVisibleStrings = shouldCheckVisibleStrings(file)

  function report(node, message) {
    failures.push(`${location(sourceFile, node)} ${message}`)
  }

  function visit(node) {
    if (ts.isCallExpression(node) && isTCall(node) && node.arguments.length) {
      const key = staticText(node.arguments[0])
      if (key !== null && cyrillic.test(key)) {
        const usages = requiredEnglishKeys.get(key) ?? []
        usages.push(location(sourceFile, node.arguments[0]))
        requiredEnglishKeys.set(key, usages)
      }
    }

    if (checkVisibleStrings && ts.isJsxText(node) && cyrillic.test(node.text)) {
      report(node, 'visible Cyrillic JSX text must be rendered through t()')
      return
    }

    if (checkVisibleStrings && ts.isTemplateExpression(node)) {
      const literalText = [node.head.text, ...node.templateSpans.map(span => span.literal.text)].join('')
      if (cyrillic.test(literalText)) report(node, 'Cyrillic template text must be rendered through t()')
      for (const span of node.templateSpans) visit(span.expression)
      return
    }

    if (checkVisibleStrings && ts.isStringLiteralLike(node) && cyrillic.test(node.text) && !isStaticTKey(node)) {
      if (isInsideJsx(node) || isInsideCallArgument(node)) {
        report(node, 'Cyrillic UI string must be wrapped in t()')
      } else {
        // Label/meta maps may hold stable translation keys for a later dynamic
        // t(value) call, but those keys must still exist in the EN dictionary.
        const usages = requiredEnglishKeys.get(node.text) ?? []
        usages.push(location(sourceFile, node))
        requiredEnglishKeys.set(node.text, usages)
      }
      return
    }

    ts.forEachChild(node, visit)
  }

  visit(sourceFile)
}

for (const [key, usages] of requiredEnglishKeys) {
  const english = translations.get(key)
  if (!english) {
    for (const usage of usages) failures.push(`${usage} missing English translation for t(${JSON.stringify(key)})`)
  } else if (cyrillic.test(english)) {
    for (const usage of usages) failures.push(`${usage} English translation still contains Cyrillic for t(${JSON.stringify(key)})`)
  }
}

if (failures.length) {
  console.error(`i18n check failed with ${failures.length} problem${failures.length === 1 ? '' : 's'}:`)
  for (const failure of failures) console.error(`  - ${failure}`)
  process.exit(1)
}

console.log(`i18n check passed: ${files.length} TS/TSX files checked, ${requiredEnglishKeys.size} translated Cyrillic keys verified.`)
