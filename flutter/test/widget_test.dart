import 'package:flutter_test/flutter_test.dart';
import 'package:hs_poc/main.dart';

void main() {
  testWidgets('HsPocApp renders', (WidgetTester tester) async {
    await tester.pumpWidget(const HsPocApp());
    expect(find.text('HS-PoC Mesh'), findsOneWidget);
  });
}