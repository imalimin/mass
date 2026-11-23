package com.afinn.mass

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import android.net.Uri
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.IntrinsicSize
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.ClickableText
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.afinn.mass.ui.theme.MassColors

private sealed interface MdBlock {
    data class Heading(val level: Int, val text: String) : MdBlock
    data class Paragraph(val text: String) : MdBlock
    data class CodeBlock(val language: String, val code: String) : MdBlock
    data class BlockQuote(val lines: List<String>) : MdBlock
    data class ListItem(val marker: String, val text: String, val indent: Int) : MdBlock
    data class Table(val headers: List<String>, val rows: List<List<String>>) : MdBlock
    data object Divider : MdBlock
}

@Composable
fun MassMarkdown(
    text: String,
    modifier: Modifier = Modifier
) {
    val blocks = remember(text) { parseMarkdownBlocks(text) }
    val context = LocalContext.current

    SelectionContainer {
        Column(
            modifier = modifier.fillMaxWidth(),
            verticalArrangement = Arrangement.spacedBy(8.dp)
        ) {
            blocks.forEach { block ->
                when (block) {
                    is MdBlock.Heading -> {
                        val size = when (block.level) {
                            1 -> 18.sp
                            2 -> 16.sp
                            3 -> 14.5.sp
                            else -> 13.5.sp
                        }
                        MarkdownInlineText(
                            raw = block.text,
                            style = TextStyle(
                                color = MassColors.Text,
                                fontSize = size,
                                fontWeight = FontWeight.SemiBold,
                                lineHeight = size * 1.45f
                            ),
                            onOpenUrl = { openExternalUrl(context, it) }
                        )
                    }

                    is MdBlock.Paragraph -> {
                        MarkdownInlineText(
                            raw = block.text,
                            style = TextStyle(
                                color = MassColors.Text,
                                fontSize = 14.5.sp,
                                lineHeight = 23.sp
                            ),
                            onOpenUrl = { openExternalUrl(context, it) }
                        )
                    }

                    is MdBlock.CodeBlock -> {
                        MarkdownCodeBlock(language = block.language, code = block.code)
                    }

                    is MdBlock.BlockQuote -> {
                        Row(
                            modifier = Modifier
                                .fillMaxWidth()
                                .height(IntrinsicSize.Min)
                        ) {
                            Box(
                                modifier = Modifier
                                    .width(3.dp)
                                    .fillMaxHeight()
                                    .background(MassColors.Focus.copy(alpha = 0.7f), RoundedCornerShape(2.dp))
                            )
                            Spacer(modifier = Modifier.width(10.dp))
                            MarkdownInlineText(
                                raw = block.lines.joinToString("\n"),
                                style = TextStyle(
                                    color = MassColors.Muted,
                                    fontSize = 14.sp,
                                    lineHeight = 22.sp
                                ),
                                onOpenUrl = { openExternalUrl(context, it) }
                            )
                        }
                    }

                    is MdBlock.ListItem -> {
                        Row(
                            modifier = Modifier
                                .fillMaxWidth()
                                .padding(start = (block.indent * 12).dp),
                            verticalAlignment = Alignment.Top
                        ) {
                            Text(
                                text = block.marker,
                                color = MassColors.AccentText,
                                fontSize = 14.sp,
                                lineHeight = 22.sp,
                                modifier = Modifier.padding(end = 8.dp)
                            )
                            MarkdownInlineText(
                                raw = block.text,
                                style = TextStyle(
                                    color = MassColors.Text,
                                    fontSize = 14.5.sp,
                                    lineHeight = 22.5.sp
                                ),
                                onOpenUrl = { openExternalUrl(context, it) },
                                modifier = Modifier.weight(1f)
                            )
                        }
                    }

                    is MdBlock.Table -> {
                        MarkdownTable(headers = block.headers, rows = block.rows)
                    }

                    MdBlock.Divider -> {
                        HorizontalDivider(
                            color = MassColors.Border,
                            thickness = 1.dp,
                            modifier = Modifier.padding(vertical = 4.dp)
                        )
                    }
                }
            }
        }
    }
}

@Composable
private fun MarkdownCodeBlock(language: String, code: String) {
    val context = LocalContext.current
    var copied by remember(code) { mutableStateOf(false) }

    Surface(
        color = MassColors.SurfaceRaised,
        shape = RoundedCornerShape(10.dp),
        border = BorderStroke(1.dp, MassColors.Border.copy(alpha = 0.7f)),
        modifier = Modifier.fillMaxWidth()
    ) {
        Column(modifier = Modifier.fillMaxWidth()) {
            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .background(MassColors.Surface)
                    .padding(start = 12.dp, end = 4.dp, top = 2.dp, bottom = 2.dp),
                horizontalArrangement = Arrangement.SpaceBetween,
                verticalAlignment = Alignment.CenterVertically
            ) {
                Text(
                    text = language.ifBlank { "code" },
                    color = MassColors.Muted,
                    fontSize = 11.5.sp,
                    fontFamily = FontFamily.Monospace
                )
                TextButton(
                    onClick = {
                        val cm = context.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
                        cm.setPrimaryClip(ClipData.newPlainText("code", code))
                        copied = true
                    }
                ) {
                    Text(
                        text = if (copied) "已复制" else "复制",
                        color = if (copied) MassColors.Success else MassColors.AccentText,
                        fontSize = 11.5.sp
                    )
                }
            }
            Box(
                modifier = Modifier
                    .fillMaxWidth()
                    .horizontalScroll(rememberScrollState())
                    .padding(12.dp)
            ) {
                Text(
                    text = code,
                    color = MassColors.Text,
                    fontSize = 12.5.sp,
                    lineHeight = 20.sp,
                    fontFamily = FontFamily.Monospace
                )
            }
        }
    }
}

@Composable
private fun MarkdownTable(headers: List<String>, rows: List<List<String>>) {
    val colCount = maxOf(headers.size, rows.maxOfOrNull { it.size } ?: 0)
    if (colCount == 0) return

    Surface(
        color = MassColors.Surface,
        shape = RoundedCornerShape(8.dp),
        border = BorderStroke(1.dp, MassColors.Border),
        modifier = Modifier
            .fillMaxWidth()
            .horizontalScroll(rememberScrollState())
    ) {
        Column {
            Row(modifier = Modifier.background(MassColors.SurfaceRaised)) {
                for (c in 0 until colCount) {
                    Box(
                        modifier = Modifier
                            .width(136.dp)
                            .padding(horizontal = 10.dp, vertical = 8.dp)
                    ) {
                        Text(
                            text = headers.getOrElse(c) { "" },
                            color = MassColors.Text,
                            fontSize = 12.5.sp,
                            fontWeight = FontWeight.SemiBold
                        )
                    }
                }
            }
            rows.forEach { row ->
                HorizontalDivider(color = MassColors.Border, thickness = 1.dp)
                Row {
                    for (c in 0 until colCount) {
                        Box(
                            modifier = Modifier
                                .width(136.dp)
                                .padding(horizontal = 10.dp, vertical = 7.dp)
                        ) {
                            Text(
                                text = row.getOrElse(c) { "" },
                                color = MassColors.Text,
                                fontSize = 12.5.sp,
                                lineHeight = 18.sp
                            )
                        }
                    }
                }
            }
        }
    }
}

@Suppress("DEPRECATION")
@Composable
private fun MarkdownInlineText(
    raw: String,
    style: TextStyle,
    onOpenUrl: (String) -> Unit,
    modifier: Modifier = Modifier
) {
    val annotated = remember(raw) { parseInlineMarkdown(raw) }
    val hasLinks = remember(annotated) {
        annotated.getStringAnnotations(tag = "URL", start = 0, end = annotated.length).isNotEmpty()
    }
    if (hasLinks) {
        ClickableText(
            text = annotated,
            style = style,
            modifier = modifier,
            onClick = { offset ->
                annotated.getStringAnnotations(tag = "URL", start = offset, end = offset)
                    .firstOrNull()?.let { onOpenUrl(it.item) }
            }
        )
    } else {
        Text(
            text = annotated,
            style = style,
            modifier = modifier
        )
    }
}

private fun openExternalUrl(context: Context, url: String) {
    if (url.startsWith("http://", ignoreCase = true) || url.startsWith("https://", ignoreCase = true)) {
        runCatching {
            context.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url)))
        }
    }
}

private val headingRegex = Regex("""^(#{1,6})\s+(.*)$""")
private val unorderedListRegex = Regex("""^(\s*)[-*+]\s+(.*)$""")
private val orderedListRegex = Regex("""^(\s*)(\d+\.)\s+(.*)$""")
private val hrRegex = Regex("""^\s*([-*_])(\s*\1){2,}\s*$""")
private val tableSeparatorRegex = Regex("""^\s*\|?(\s*:?-{3,}:?\s*\|)+\s*:?-{3,}:?\s*\|?\s*$""")

private fun parseMarkdownBlocks(input: String): List<MdBlock> {
    val lines = input.replace("\r\n", "\n").split("\n")
    val blocks = mutableListOf<MdBlock>()
    val paragraphBuf = mutableListOf<String>()
    var i = 0

    fun flushParagraph() {
        if (paragraphBuf.isNotEmpty()) {
            blocks.add(MdBlock.Paragraph(paragraphBuf.joinToString("\n")))
            paragraphBuf.clear()
        }
    }

    while (i < lines.size) {
        val line = lines[i]
        val trimmed = line.trim()

        if (trimmed.isEmpty()) {
            flushParagraph()
            i++
            continue
        }

        // Fenced code block
        if (trimmed.startsWith("```")) {
            flushParagraph()
            val lang = trimmed.removePrefix("```").trim()
            val codeLines = mutableListOf<String>()
            i++
            while (i < lines.size && !lines[i].trim().startsWith("```")) {
                codeLines.add(lines[i])
                i++
            }
            if (i < lines.size) i++ // consume closing ```
            blocks.add(MdBlock.CodeBlock(lang, codeLines.joinToString("\n")))
            continue
        }

        // Heading
        val headingMatch = headingRegex.matchEntire(trimmed)
        if (headingMatch != null) {
            flushParagraph()
            blocks.add(MdBlock.Heading(headingMatch.groupValues[1].length, headingMatch.groupValues[2].trim()))
            i++
            continue
        }

        // Horizontal rule
        if (hrRegex.matches(line)) {
            flushParagraph()
            blocks.add(MdBlock.Divider)
            i++
            continue
        }

        // Table
        if (line.contains('|') && i + 1 < lines.size && tableSeparatorRegex.matches(lines[i + 1])) {
            flushParagraph()
            val headers = splitTableRow(line)
            i += 2
            val rows = mutableListOf<List<String>>()
            while (i < lines.size && lines[i].contains('|') && lines[i].trim().isNotEmpty()) {
                rows.add(splitTableRow(lines[i]))
                i++
            }
            blocks.add(MdBlock.Table(headers, rows))
            continue
        }

        // Blockquote
        if (trimmed.startsWith(">")) {
            flushParagraph()
            val quoteLines = mutableListOf<String>()
            while (i < lines.size && lines[i].trim().startsWith(">")) {
                quoteLines.add(lines[i].trim().removePrefix(">").trimStart())
                i++
            }
            blocks.add(MdBlock.BlockQuote(quoteLines))
            continue
        }

        // Unordered list item
        val unorderedMatch = unorderedListRegex.matchEntire(line)
        if (unorderedMatch != null) {
            flushParagraph()
            val indent = (unorderedMatch.groupValues[1].length / 2).coerceAtMost(4)
            blocks.add(MdBlock.ListItem("•", unorderedMatch.groupValues[2], indent))
            i++
            continue
        }

        // Ordered list item
        val orderedMatch = orderedListRegex.matchEntire(line)
        if (orderedMatch != null) {
            flushParagraph()
            val indent = (orderedMatch.groupValues[1].length / 2).coerceAtMost(4)
            blocks.add(MdBlock.ListItem(orderedMatch.groupValues[2], orderedMatch.groupValues[3], indent))
            i++
            continue
        }

        paragraphBuf.add(line)
        i++
    }
    flushParagraph()
    return blocks
}

private fun splitTableRow(line: String): List<String> {
    var s = line.trim()
    if (s.startsWith("|")) s = s.substring(1)
    if (s.endsWith("|")) s = s.substring(0, s.length - 1)
    return s.split('|').map { it.trim() }
}

private val inlineTokenRegex = Regex(
    """`([^`\n]+)`|\[([^\]]+)]\((https?://[^)\s]+)\)|\*\*([^*\n]+)\*\*|__([^_\n]+)__|~~([^~\n]+)~~|\*([^*\n]+)\*"""
)

private fun parseInlineMarkdown(text: String): AnnotatedString = buildAnnotatedString {
    var cursor = 0
    for (match in inlineTokenRegex.findAll(text)) {
        if (match.range.first > cursor) {
            append(text.substring(cursor, match.range.first))
        }
        val code = match.groups[1]?.value
        val linkLabel = match.groups[2]?.value
        val linkUrl = match.groups[3]?.value
        val boldStar = match.groups[4]?.value
        val boldUnder = match.groups[5]?.value
        val strike = match.groups[6]?.value
        val italic = match.groups[7]?.value

        when {
            code != null -> {
                pushStyle(
                    SpanStyle(
                        color = MassColors.AccentText,
                        background = MassColors.SurfaceRaised,
                        fontFamily = FontFamily.Monospace,
                        fontSize = 13.sp
                    )
                )
                append(" $code ")
                pop()
            }
            linkLabel != null && linkUrl != null -> {
                pushStringAnnotation(tag = "URL", annotation = linkUrl)
                pushStyle(
                    SpanStyle(
                        color = MassColors.AccentText,
                        textDecoration = TextDecoration.Underline
                    )
                )
                append(linkLabel)
                pop()
                pop()
            }
            boldStar != null || boldUnder != null -> {
                pushStyle(SpanStyle(fontWeight = FontWeight.SemiBold, color = MassColors.Text))
                append(boldStar ?: boldUnder.orEmpty())
                pop()
            }
            strike != null -> {
                pushStyle(
                    SpanStyle(
                        textDecoration = TextDecoration.LineThrough,
                        color = MassColors.Muted
                    )
                )
                append(strike)
                pop()
            }
            italic != null -> {
                pushStyle(SpanStyle(fontStyle = FontStyle.Italic))
                append(italic)
                pop()
            }
            else -> append(match.value)
        }
        cursor = match.range.last + 1
    }
    if (cursor < text.length) {
        append(text.substring(cursor))
    }
}
